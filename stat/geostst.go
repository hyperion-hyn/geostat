package stat

import (
	"fmt"
	"github.com/hpcloud/tail"
	client "github.com/influxdata/influxdb1-client/v2"
	"github.com/mmcloughlin/geohash"
	"github.com/oschwald/geoip2-golang"
	"github.com/spf13/viper"
	"log"
	"net"
	"os"
	"regexp"
	"time"
)

type geoInfo struct {
	geohash      string
	host         string
	ip           string
	country_code string
	city         string
}

var createdDB = false

func Stat(logFile, geoDB string) error {
	c, err := client.NewUDPClient(client.UDPConfig{
		Addr: fmt.Sprintf("%s:%s", viper.GetString("db.host"), viper.GetString("db.port")),
	})
	defer c.Close()
	if err != nil {
		log.Panicf("Error creating InfluxDB Client: %v", err.Error())
	}

	geoDBC, err := geoip2.Open(geoDB)
	defer geoDBC.Close()
	if err != nil {
		log.Panicf("error, open GeoLite2-City.mmdb get error, %v\n", err)
	}

	t, err := tail.TailFile(logFile, tail.Config{
		Follow:    true,
		ReOpen:    true,
		MustExist: false,
		Location:  &tail.SeekInfo{Offset: 0, Whence: 2},
		Poll:      true,
	})

	if err != nil {
		log.Panicf("tail file err:%v\n", err)
	}

	var (
		pts                = make([]*client.Point, 0)
		lastDataTime       = time.Now().Unix()
	)
	for line := range t.Lines {
		if line.Err != nil {
			log.Println(fmt.Sprintf("tail file get error, reopen %s, err: %s\n", t.Filename, line.Err))
			time.Sleep(100 * time.Millisecond)
			continue
		}

		ipRegexp := regexp.MustCompile(`([0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3})`)
		ipStr := ipRegexp.FindString(line.Text)

		if ipStr == "" {
			continue
		}

		ip := net.ParseIP(ipStr)
		if !isPublicIP(ip) {
			continue
		}

		geoinfo, err := geostat(ip, geoDBC)
		if err != nil {
			continue
		}

		pts, err = saveToInfluxd(pts, c, geoinfo, lastDataTime)
		if err != nil {
			continue
		}
		lastDataTime = time.Now().Unix()
	}

	return nil
}

func geostat(ip net.IP, geoDBC *geoip2.Reader) (geoInfo, error) {
	geo := geoInfo{}
	record, err := geoDBC.City(ip)
	if err != nil {
		fmt.Printf("error, parsing IP geo info get error, %v\n", err)

		return geo, err
	}

	host, err := os.Hostname()
	if err != nil {
		fmt.Printf("error, get os hostname get error, %v\n", err)

		return geo, err
	}

	city := ""
	if record.City.Names["en"] != "" {
		city = record.City.Names["en"]
	} else {
		city = record.Country.Names["en"]
	}

	geo = geoInfo{
		geohash:      geohash.Encode(record.Location.Latitude, record.Location.Longitude),
		host:         host,
		ip:           ip.String(),
		country_code: record.Country.IsoCode,
		city:         city,
	}

	return geo, nil
}

func isPublicIP(IP net.IP) bool {
	if IP.IsLoopback() || IP.IsLinkLocalMulticast() || IP.IsLinkLocalUnicast() {
		return false
	}
	if ip4 := IP.To4(); ip4 != nil {
		switch true {
		case ip4[0] == 10:
			return false
		case ip4[0] == 172 && ip4[1] >= 16 && ip4[1] <= 31:
			return false
		case ip4[0] == 192 && ip4[1] == 168:
			return false
		default:
			return true
		}
	}

	return false
}

func createInfluxDB() {
	c, err := client.NewHTTPClient(client.HTTPConfig{
		Addr: fmt.Sprintf("http://%s:%s", viper.GetString("db.host"), viper.GetString("db.tcp_port")),
	})
	if err != nil {
		log.Panicf("Error creating InfluxDB Client: %v", err.Error())
	}
	defer c.Close()

	q := client.NewQuery(fmt.Sprintf("CREATE DATABASE %s", viper.GetString("db.database")), "", "")
	if response, err := c.Query(q); err != nil || response.Error() != nil {
		log.Panicf("Error creating InfluxDB databsse: %v, %v", err, response.Error())
	}

	q = client.NewQuery(
		fmt.Sprintf("CREATE RETENTION POLICY \"%s\" ON \"%s\" DURATION %s REPLICATION 1 DEFAULT",
			viper.GetString("db.retention_policy.name"),
			viper.GetString("db.database"),
			viper.GetString("db.retention_policy.value")), "", "")
	if response, err := c.Query(q); err != nil || response.Error() != nil {
		log.Panicf("Error creating InfluxDB databsse retention policy, db: %s, %v, %v",
			viper.GetString("db.database"),
			err,
			response.Error())
	}

	createdDB = true
}

func saveToInfluxd(pts []*client.Point, c client.Client, geoinfo geoInfo, lastDataTime int64) ([]*client.Point, error) {
	pt, err := client.NewPoint(viper.GetString("db.measurement"), map[string]string{
		"geohash":      geoinfo.geohash,
		"host":         geoinfo.host,
		"ip":           geoinfo.ip,
		"country_code": geoinfo.country_code,
		"city":         geoinfo.city,
	}, map[string]interface{}{
		"count": 1,
	}, time.Now())

	if err != nil {
		log.Printf("new influxdb PT get error:%v\n", err)

		return pts, err
	}
	pts = append(pts, pt)

	timeInt := time.Now().Unix() - lastDataTime
	if len(pts) >= viper.GetInt("db.full_size") || timeInt >= viper.GetInt64("db.insert_tim_int") {
		if !createdDB {
			createInfluxDB()
		}
		// insert into influxd
		bp, err := client.NewBatchPoints(client.BatchPointsConfig{
			Database: viper.GetString("db.database"),
		})

		if err != nil {
			log.Printf("new influxd bath points get error: %v", err)

			return pts, err
		}

		bp.AddPoints(pts)

		if err := c.Write(bp); err != nil {
			log.Printf("write influxdb data get error:%v\n", err)

			return pts, err
		}

		// reset pts
		pts = make([]*client.Point, 0)
	}

	lastDataTime = time.Now().Unix()

	return pts, nil
}
