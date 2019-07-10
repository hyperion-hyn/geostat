package stat

import (
	"fmt"
	"github.com/spf13/viper"
	"log"
	"net"
	"os"
	"regexp"
	"time"

	"github.com/hpcloud/tail"
	"github.com/influxdata/influxdb1-client/v2"
	"github.com/mmcloughlin/geohash"
	"github.com/oschwald/geoip2-golang"
)

type geoInfo struct {
	geohash      string
	host         string
	ip           string
	country_code string
	city         string
}

func Stat(logFile, geoDB string) error {
	c, err := client.NewHTTPClient(client.HTTPConfig{
		Addr:     fmt.Sprintf("http://%s:%s", viper.GetString("db.host"), viper.GetString("db.port")),
		Username: viper.GetString("db.username"),
		Password: viper.GetString("db.password"),
	})
	if err != nil {
		log.Panicf("Error creating InfluxDB Client: %v", err.Error())
	}

	defer c.Close()

	bp, err := client.NewBatchPoints(client.BatchPointsConfig{
		Database:  viper.GetString("db.database"),
		Precision: "s",
	})

	t, err := tail.TailFile(logFile, tail.Config{
		Follow:    true,
		ReOpen:    true,
		MustExist: false,
		Location:  &tail.SeekInfo{Offset: 0, Whence: 2},
		Poll:      true,
	})

	if err != nil {
		fmt.Println("tail file err:", err)
		return err
	}

	var msg *tail.Line
	var ok bool
	for true {
		msg, ok = <-t.Lines
		if !ok {
			fmt.Printf("tail file closed and reopen, filename:%s\n", t.Filename)
			time.Sleep(100 * time.Millisecond)
			continue
		}

		ipRegexp := regexp.MustCompile(`([0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3})`)
		ipStr := ipRegexp.FindString(msg.Text)

		if ipStr == "" {
			continue
		}

		ip := net.ParseIP(ipStr)
		if !isPublicIP(ip) {
			continue
		}

		geoinfo, err := geostat(ip, geoDB)
		if err != nil {
			continue
		}

		tags := map[string]string{
			"geohash":      geoinfo.geohash,
			"host":         geoinfo.host,
			"ip":           geoinfo.ip,
			"country_code": geoinfo.country_code,
			"city":         geoinfo.city,
		}
		fields := map[string]interface{}{
			"count": 1,
		}
		pt, err := client.NewPoint(viper.GetString("db.measurement"), tags, fields, time.Now())
		if err != nil {
			fmt.Printf("new influxdb PT get error:%v\n", err)
			continue
		}
		bp.AddPoint(pt)

		if err := c.Write(bp); err != nil {
			fmt.Printf("write influxdb data get error:%v\n", err)
			continue
		}
	}

	return nil
}

func geostat(ip net.IP, geoDB string) (geoInfo, error) {
	geo := geoInfo{}
	db, err := geoip2.Open(geoDB)
	if err != nil {
		fmt.Printf("error, open GeoLite2-City.mmdb get error, %v\n", err)

		return geo, err
	}
	defer db.Close()

	record, err := db.City(ip)
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
