package drain

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"code.cloudfoundry.org/lager"
	"github.com/cloudfoundry-community/splunk-firehose-nozzle/splunk"
)

type LoggingConfig struct {
	FlushInterval time.Duration
	QueueSize     int //consumer queue buffer size
	BatchSize     int
	Retries       int //No of retries to post events to HEC before dropping events
}

type LoggingSplunk struct {
	logger  lager.Logger
	clients []splunk.SplunkClient
	config  *LoggingConfig
	events  chan map[string]interface{}
}

func NewLoggingSplunk(logger lager.Logger, splunkClients []splunk.SplunkClient, config *LoggingConfig) *LoggingSplunk {
	return &LoggingSplunk{
		logger:  logger,
		clients: splunkClients,
		config:  config,
		events:  make(chan map[string]interface{}, config.QueueSize),
	}
}

func (l *LoggingSplunk) Connect() bool {
	for _, client := range l.clients {
		go l.consume(client)
	}

	return true
}

func (l *LoggingSplunk) ShipEvents(fields map[string]interface{}, msg string) {
	event := l.buildEvent(fields, msg)
	l.events <- event
}

func (l *LoggingSplunk) consume(client splunk.SplunkClient) {
	var batch []map[string]interface{}
	tickChan := time.NewTicker(l.config.FlushInterval).C

	// Either flush window or batch size reach limits, we flush
	for {
		select {
		case event := <-l.events:
			batch = append(batch, event)
			if len(batch) >= l.config.BatchSize {
				batch = l.indexEvents(client, batch)
			}
		case <-tickChan:
			batch = l.indexEvents(client, batch)
		}
	}
}

// indexEvents indexes events to Splunk
// return nil when sucessful which clears all outstanding events
// return what the batch has if there is an error for next retry cycle
func (l *LoggingSplunk) indexEvents(client splunk.SplunkClient, batch []map[string]interface{}) []map[string]interface{} {
	if len(batch) == 0 {
		return batch
	}
	var err error
	for i := 0; i < l.config.Retries; i++ {
		// l.logger.Info(fmt.Sprintf("Posting %d events", len(batch)))
		err = client.Post(batch)
		if err == nil {
			return nil
		}
		l.logger.Error("Unable to talk to Splunk", err)
		time.Sleep(5 * time.Second)
	}
	l.logger.Error("Finish retrying and dropping events", err, lager.Data{"events": len(batch)})
	return nil
}

func (l *LoggingSplunk) buildEvent(fields map[string]interface{}, msg string) map[string]interface{} {
	if len(msg) > 0 {
		fields["msg"] = msg
	}
	event := map[string]interface{}{}

	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	if val, ok := fields["timestamp"]; ok {
		timestamp = l.nanoSecondsToSeconds(val.(int64))
	}
	event["time"] = timestamp

	event["host"] = fields["ip"]
	event["source"] = fields["job"]

	eventType := strings.ToLower(fields["event_type"].(string))
	event["sourcetype"] = fmt.Sprintf("cf:%s", eventType)

	event["event"] = fields

	return event
}

func (l *LoggingSplunk) nanoSecondsToSeconds(nanoseconds int64) string {
	seconds := float64(nanoseconds) * math.Pow(1000, -3)
	return fmt.Sprintf("%.3f", seconds)
}
