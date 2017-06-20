package firehoseclient

import (
	"crypto/tls"
	"time"

	"github.com/cloudfoundry-community/firehose-to-syslog/eventRouting"
	"github.com/cloudfoundry-community/firehose-to-syslog/logging"
	"github.com/cloudfoundry/noaa/consumer"
	"github.com/cloudfoundry/sonde-go/events"
	"github.com/gorilla/websocket"
)

type FirehoseNozzle struct {
	errs           <-chan error
	messages       <-chan *events.Envelope
	consumer       *consumer.Consumer
	eventRouting   *eventRouting.EventRouting
	config         *FirehoseConfig
	tokenRefresher consumer.TokenRefresher
}

type FirehoseConfig struct {
	TrafficControllerURL   string
	InsecureSSLSkipVerify  bool
	IdleTimeoutSeconds     time.Duration
	FirehoseSubscriptionID string
}

func NewFirehoseNozzle(tokenRefresher consumer.TokenRefresher, eventRouting *eventRouting.EventRouting, firehoseconfig *FirehoseConfig) *FirehoseNozzle {
	return &FirehoseNozzle{
		errs:           make(<-chan error),
		messages:       make(<-chan *events.Envelope),
		eventRouting:   eventRouting,
		config:         firehoseconfig,
		tokenRefresher: tokenRefresher,
	}
}

func (f *FirehoseNozzle) Start() error {
	f.consumeFirehose()
	err := f.routeEvent()
	return err
}

func (f *FirehoseNozzle) consumeFirehose() {
	f.consumer = consumer.New(
		f.config.TrafficControllerURL,
		&tls.Config{
			InsecureSkipVerify: f.config.InsecureSSLSkipVerify,
		},
		nil,
	)
	f.consumer.RefreshTokenFrom(f.tokenRefresher)
	f.consumer.SetIdleTimeout(time.Duration(f.config.IdleTimeoutSeconds) * time.Second)
	f.messages, f.errs = f.consumer.Firehose(f.config.FirehoseSubscriptionID, "")
}

func (f *FirehoseNozzle) routeEvent() error {
	for {
		select {
		case envelope := <-f.messages:
			f.eventRouting.RouteEvent(envelope)
		case err := <-f.errs:
			f.handleError(err)
			continue // no op
		}
	}
}

func (f *FirehoseNozzle) handleError(err error) {

	switch closeErr := err.(type) {
        case *websocket.CloseError:
                switch closeErr.Code {
                	case websocket.CloseNormalClosure:
                	// no op
               	 	case websocket.ClosePolicyViolation:
						logging.LogError("Error while reading from the firehose: %v", err)
						logging.LogError("Disconnected because nozzle couldn't keep up. Please try scaling up the nozzle.", nil)
                default:
                        logging.LogError("Error while reading from the firehose: %v", err)
                }
        default:
			logging.LogError("Error while reading from the firehose: %v", err)
    }

	logging.LogError("Closing connection with traffic controller due to %v", err)
	// no op
}
