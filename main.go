package main

import (
	"flag"
	"fmt"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	log "github.com/sirupsen/logrus"
	"github.com/wasilak/aws-autoscaling-exporter/exporter"
	"strings"
)

type regionSlice []string

type Value interface {
	String() string
	Set(string) error
}

func (i *regionSlice) String() string {
	return fmt.Sprintf("%d", *i)
}

func (i *regionSlice) Set(value string) error {
	fmt.Printf("%s\n", value)
	*i = append(*i, value)
	return nil
}

var (
	addr        = flag.String("listen-address", ":8089", "The address to listen on for HTTP requests.")
	metricsPath = flag.String("metrics-path", "/metrics", "path to metrics endpoint")
	rawLevel    = flag.String("log-level", "info", "log level")
	regionsFlag = flag.String("regions", "", "Comma separated list of regions")
	groupsFlag  = flag.String("auto-scaling-groups", "", "Comma separated list of auto scaling groups to monitor. Empty value means all groups in the region.")
)

func init() {
	var regions regionSlice

	flag.Var(&regions, "region", "AWS region that the exporter should query")

	flag.Parse()
	parsedLevel, err := log.ParseLevel(*rawLevel)
	if err != nil {
		log.WithError(err).Warnf("Couldn't parse log level, using default: %s", log.GetLevel())
	} else {
		log.SetLevel(parsedLevel)
		log.Debugf("Set log level to %s", parsedLevel)
	}
}

func main() {
	log.Info("Starting AWS Auto Scaling Group exporter")
	log.Infof("Starting metric http endpoint on %s", *addr)

	var groups []string
	if *groupsFlag != "" {
		groups = strings.Split(strings.Replace(*groupsFlag, " ", "", -1), ",")
	}

	var regions []string
	if *regionsFlag != "" {
		regions = strings.Split(strings.Replace(*regionsFlag, " ", "", -1), ",")
	}

	exporter, err := exporter.NewExporter(regions, groups)
	if err != nil {
		log.Fatal(err)
	}

	prometheus.MustRegister(exporter)

	http.Handle(*metricsPath, promhttp.Handler())
	http.HandleFunc("/", rootHandler)
	log.Fatal(http.ListenAndServe(*addr, nil))
}

func rootHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`<html>
		<head><title>AWS Auto Scaling Group Exporter</title></head>
		<body>
		<h1>AWS Auto Scaling Group Exporter</h1>
		<p><a href="` + *metricsPath + `">Metrics</a></p>
		</body>
		</html>
	`))

}
