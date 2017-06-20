package caching

import (
	"fmt"
	"github.com/boltdb/bolt"
	"github.com/cloudfoundry-community/firehose-to-syslog/logging"
	cfClient "github.com/cloudfoundry-community/go-cfclient"
	json "github.com/mailru/easyjson"
	"log"
	"os"
	"time"
	"golang.org/x/sync/syncmap"
)

type CachingBolt struct {
	GcfClient *cfClient.Client
	Appdb     *bolt.DB
	missed	  *syncmap.Map
}

func NewCachingBolt(gcfClientSet *cfClient.Client, boltDatabasePath string) Caching {

	//Use bolt for in-memory  - file caching
	db, err := bolt.Open(boltDatabasePath, 0600, &bolt.Options{Timeout: 1 * time.Second})
	if err != nil {
		log.Fatal("Error opening bolt db: ", err)
		os.Exit(1)

	}

	cachingBolt := &CachingBolt{
		GcfClient: gcfClientSet,
		Appdb:     db,
		missed:    new(syncmap.Map),
	}

	ticker := time.Tick(1 * time.Hour)

	go func() {
		for range ticker {
			cachingBolt.missed = new(syncmap.Map)
		}
	}()

	return cachingBolt
}

func (c *CachingBolt) CreateBucket() {
	c.Appdb.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists([]byte("AppBucket"))
		if err != nil {
			return fmt.Errorf("create bucket: %s", err)
		}
		return nil

	})
}

func (c *CachingBolt) PerformPoollingCaching(tickerTime time.Duration) {
	// Ticker Pooling the CC every X sec
	ccPooling := time.NewTicker(tickerTime)

	var apps []App
	go func() {
		for range ccPooling.C {
			apps = c.GetAllApp()
		}
	}()

}

func (c *CachingBolt) fillDatabase(listApps []App) {
	for _, app := range listApps {
		c.Appdb.Update(func(tx *bolt.Tx) error {
			b, err := tx.CreateBucketIfNotExists([]byte("AppBucket"))
			if err != nil {
				return fmt.Errorf("create bucket: %s", err)
			}

			serialize, err := json.Marshal(app)

			if err != nil {
				return fmt.Errorf("Error Marshaling data: %s", err)
			}
			err = b.Put([]byte(app.Guid), serialize)

			if err != nil {
				return fmt.Errorf("Error inserting data: %s", err)
			}
			return nil
		})

	}

}

func (c *CachingBolt) GetAppByGuid(appGuid string) []App {
	var apps []App
	app, err := c.GcfClient.AppByGuid(appGuid)
	if err != nil {
		return apps
	}
	apps = append(apps, App{app.Name, app.Guid, app.SpaceData.Entity.Name, app.SpaceData.Entity.Guid, app.SpaceData.Entity.OrgData.Entity.Name, app.SpaceData.Entity.OrgData.Entity.Guid})
	c.fillDatabase(apps)
	return apps

}

func (c *CachingBolt) GetAllApp() []App {

	logging.LogStd("Retrieving Apps for Cache...", false)
	var apps []App

	defer func() {
		if r := recover(); r != nil {
			logging.LogError("Recovered in caching.GetAllApp()", r)
		}
	}()

	cfApps, err := c.GcfClient.ListApps()
	if err != nil {
		return apps
	}

	for _, app := range cfApps {
		logging.LogStd(fmt.Sprintf("App [%s] Found...", app.Name), false)
		apps = append(apps, App{app.Name, app.Guid, app.SpaceData.Entity.Name, app.SpaceData.Entity.Guid, app.SpaceData.Entity.OrgData.Entity.Name, app.SpaceData.Entity.OrgData.Entity.Guid})
	}

	c.fillDatabase(apps)
	logging.LogStd(fmt.Sprintf("Found [%d] Apps!", len(apps)), false)

	return apps
}

func (c *CachingBolt) GetAppInfo(appGuid string) App {

	var d []byte
	var app App
	c.Appdb.View(func(tx *bolt.Tx) error {
		logging.LogStd(fmt.Sprintf("Looking for App %s in Cache!\n", appGuid), false)
		b := tx.Bucket([]byte("AppBucket"))
		d = b.Get([]byte(appGuid))
		return nil
	})
	err := json.Unmarshal([]byte(d), &app)
	if err != nil {
		return App{}
	}
	return app
}

func (c *CachingBolt) Close() {
	c.Appdb.Close()
}

func (c *CachingBolt) GetAppInfoCache(appGuid string) App {

	// App exists in cache
	if app := c.GetAppInfo(appGuid); app.Name != "" {
		return app
	}

	// App already missed, ignore
	if _, found := c.missed.Load(appGuid); found {
		return App{}
	}

	// First time seeing app
	apps:=c.GetAppByGuid(appGuid)
	if apps[0].Name == ""{

		// Remember not to look up this app again
		c.missed.Store(appGuid,0)
		return App{}
	}
	return apps[0]
}
