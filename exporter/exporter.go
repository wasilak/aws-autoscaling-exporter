package exporter

import (
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/autoscaling"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/prometheus/client_golang/prometheus"
	log "github.com/sirupsen/logrus"
)

// Exporter implements the prometheus.Exporter interface, and exports AWS AutoScaling metrics.
type Exporter struct {
	sessions        []*session.Session
	groups          []string
	metricsName     string
	duration        prometheus.Gauge
	scrapeErrors    prometheus.Gauge
	totalScrapes    prometheus.Counter
	groupMetrics    map[string]*prometheus.GaugeVec
	instanceMetrics map[string]*prometheus.GaugeVec
	metricsMtx      sync.RWMutex
	sync.RWMutex
}

type GroupScrapeResult struct {
	Name             string
	Value            float64
	AutoScalingGroup string
	Region           string
	Prefix           string
}

type InstanceScrapeResult struct {
	Name             string
	Value            float64
	AutoScalingGroup string
	Region           string
	InstanceId       string
	AvailabilityZone string
	InstanceType     string
	Prefix           string
}

type Recommendation map[string][]InstanceTypeRecommendation

type InstanceTypeRecommendation struct {
	InstanceTypeName   string `json:"InstanceTypeName"`
	CurrentPrice       string `json:"CurrentPrice"`
	AvgPriceFor24Hours string `json:"AvgPriceFor24Hours"`
	OnDemandPrice      string `json:"OnDemandPrice"`
	SuggestedBidPrice  string `json:"SuggestedBidPrice"`
	CostScore          string `json:"CostScore"`
	StabilityScore     string `json:"StabilityScore"`
}

type instanceScrapeError struct {
	count uint64
}

func (e *instanceScrapeError) Error() string {
	return fmt.Sprintf("Error count: %d", e.count)
}

// NewExporter returns a new exporter of AWS Autoscaling group metrics.
func NewExporter(regions []string, groups []string, metricsName string) (*Exporter, error) {

	var sessions []*session.Session

	for _, region := range regions {

		sess, err := session.NewSession(&aws.Config{
			Region: aws.String(region),
		})
		if err != nil {
			log.WithError(err).Error("Error creating AWS session")
			return nil, err
		}
		sessions = append(sessions, sess)
	}

	e := Exporter{
		sessions:    sessions,
		metricsName: metricsName,
		groups:      groups,
		duration: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "aws_autoscaling",
			Name:      "scrape_duration_seconds",
			Help:      "The scrape duration.",
		}),
		totalScrapes: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "aws_autoscaling",
			Name:      "scrapes_total",
			Help:      "Total AWS autoscaling group scrapes.",
		}),
		scrapeErrors: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "aws_autoscaling",
			Name:      "scrape_error",
			Help:      "The scrape error status.",
		}),
	}
	e.initGauges()

	return &e, nil
}

func (e *Exporter) initGauges() {
	e.groupMetrics = map[string]*prometheus.GaugeVec{}

	e.groupMetrics["pending_instances_total"] = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "aws_autoscaling",
		Name:      "pending_instances_total",
		Help:      "Total number of pending instances in the auto scaling group",
	}, []string{"asg_name", "region", "prefix"})
	e.groupMetrics["inservice_instances_total"] = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "aws_autoscaling",
		Name:      "inservice_instances_total",
		Help:      "Total number of in service instances in the auto scaling group",
	}, []string{"asg_name", "region", "prefix"})
	e.groupMetrics["standby_instances_total"] = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "aws_autoscaling",
		Name:      "standby_instances_total",
		Help:      "Total number of standby instances in the auto scaling group",
	}, []string{"asg_name", "region", "prefix"})
	e.groupMetrics["terminating_instances_total"] = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "aws_autoscaling",
		Name:      "terminating_instances_total",
		Help:      "Total number of terminating instances in the auto scaling group",
	}, []string{"asg_name", "region", "prefix"})
	e.groupMetrics["spot_instances_total"] = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "aws_autoscaling",
		Name:      "spot_instances_total",
		Help:      "Total number of spot instances in the auto scaling group",
	}, []string{"asg_name", "region", "prefix"})
	e.groupMetrics["instances_total"] = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "aws_autoscaling",
		Name:      "instances_total",
		Help:      "Total number of instances in the auto scaling group",
	}, []string{"asg_name", "region", "prefix"})

	e.instanceMetrics = map[string]*prometheus.GaugeVec{}
	e.instanceMetrics["spot_bid_price"] = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "aws_instance",
		Name:      "spot_bid_price",
		Help:      "Spot bid price used to request the spot instance",
	}, []string{"asg_name", "region", "instance_id", "instance_type", "availability_zone", "prefix"})
	e.instanceMetrics["cost_score"] = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "aws_instance",
		Name:      "cost_score",
		Help:      "Current cost score of spot instance reported by the spot recommender",
	}, []string{"asg_name", "region", "instance_id", "instance_type", "availability_zone", "prefix"})
	e.instanceMetrics["stability_score"] = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "aws_instance",
		Name:      "stability_score",
		Help:      "Current stability score of spot instance reported by the spot recommender",
	}, []string{"asg_name", "region", "instance_id", "instance_type", "availability_zone", "prefix"})
	e.instanceMetrics["on_demand_price"] = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "aws_instance",
		Name:      "on_demand_price",
		Help:      "Current on demand price of spot instance reported by the spot recommender",
	}, []string{"asg_name", "region", "instance_id", "instance_type", "availability_zone", "prefix"})
	e.instanceMetrics["optimal_bid_price"] = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "aws_instance",
		Name:      "optimal_bid_price",
		Help:      "Optimal spot bid price of instance reported by the spot recommender",
	}, []string{"asg_name", "region", "instance_id", "instance_type", "availability_zone", "prefix"})
	e.instanceMetrics["current_price"] = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "aws_instance",
		Name:      "current_price",
		Help:      "Current price of spot instance reported by the spot recommender.",
	}, []string{"asg_name", "region", "instance_id", "instance_type", "availability_zone", "prefix"})
}

// Describe outputs metric descriptions.
func (e *Exporter) Describe(ch chan<- *prometheus.Desc) {
	for _, m := range e.groupMetrics {
		m.Describe(ch)
	}
	for _, m := range e.instanceMetrics {
		m.Describe(ch)
	}
	ch <- e.duration.Desc()
	ch <- e.totalScrapes.Desc()
	ch <- e.scrapeErrors.Desc()
}

// Collect fetches info from the AWS API and the BanzaiCloud recommendation API
func (e *Exporter) Collect(ch chan<- prometheus.Metric) {

	groupScrapes := make(chan GroupScrapeResult)
	instanceScrapes := make(chan InstanceScrapeResult)

	e.Lock()
	defer e.Unlock()

	e.initGauges()
	go e.scrape(groupScrapes, instanceScrapes)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		e.setGroupMetrics(groupScrapes)
	}()
	go func() {
		defer wg.Done()
		e.setInstanceMetrics(instanceScrapes)
	}()
	wg.Wait()

	e.duration.Collect(ch)
	e.totalScrapes.Collect(ch)
	e.scrapeErrors.Collect(ch)

	for _, m := range e.groupMetrics {
		m.Collect(ch)
	}
	for _, m := range e.instanceMetrics {
		m.Collect(ch)
	}
}

func (e *Exporter) scrape(groupScrapes chan<- GroupScrapeResult, instanceScrapes chan<- InstanceScrapeResult) {

	defer close(groupScrapes)
	defer close(instanceScrapes)
	now := time.Now().UnixNano()
	e.totalScrapes.Inc()

	var errorCount uint64 = 0

	for _, sess := range e.sessions {
		asgSvc := autoscaling.New(sess, aws.NewConfig())
		err := asgSvc.DescribeAutoScalingGroupsPages(&autoscaling.DescribeAutoScalingGroupsInput{
			AutoScalingGroupNames: aws.StringSlice(e.groups),
		}, func(result *autoscaling.DescribeAutoScalingGroupsOutput, lastPage bool) bool {
			log.Debugf("Number of AutoScaling Groups found: %d [lastPage = %t]", len(result.AutoScalingGroups), lastPage)
			var wg sync.WaitGroup
			for _, asg := range result.AutoScalingGroups {
				log.Debug("scraping: ", *asg.AutoScalingGroupName)
				wg.Add(1)
				go func(asg *autoscaling.Group) {
					defer wg.Done()

					if err := e.scrapeAsg(sess, groupScrapes, instanceScrapes, asg); err != nil {
						log.WithField("autoScalingGroup", *asg.AutoScalingGroupName).Error(err)
						if e, ok := err.(*instanceScrapeError); ok {
							atomic.AddUint64(&errorCount, e.count)
						} else {
							atomic.AddUint64(&errorCount, 1)
						}

					}
				}(asg)
			}
			wg.Wait()
			return true
		})
		if err != nil {
			log.WithError(err).Error("An error happened while fetching AutoScaling Groups")
			atomic.AddUint64(&errorCount, 1)
		}

		e.scrapeErrors.Set(float64(atomic.LoadUint64(&errorCount)))
		e.duration.Set(float64(time.Now().UnixNano()-now) / 1000000000)
	}

}

func (e *Exporter) setGroupMetrics(scrapes <-chan GroupScrapeResult) {
	log.Debug("set group metrics")
	for scr := range scrapes {
		name := scr.Name
		if _, ok := e.groupMetrics[name]; !ok {
			e.metricsMtx.Lock()
			e.groupMetrics[name] = prometheus.NewGaugeVec(prometheus.GaugeOpts{
				Namespace: "aws_autoscaling",
				Name:      name,
			}, []string{"asg_name", "region", "prefix"})
			e.metricsMtx.Unlock()
		}
		var labels prometheus.Labels = map[string]string{"asg_name": scr.AutoScalingGroup, "region": scr.Region, "prefix": e.metricsName}
		e.groupMetrics[name].With(labels).Set(float64(scr.Value))
	}
}

func (e *Exporter) setInstanceMetrics(scrapes <-chan InstanceScrapeResult) {
	log.Debug("set instance metrics")
	for scr := range scrapes {
		name := scr.Name
		if _, ok := e.instanceMetrics[name]; !ok {
			e.metricsMtx.Lock()
			e.instanceMetrics[name] = prometheus.NewGaugeVec(prometheus.GaugeOpts{
				Namespace: "aws_instance",
				Name:      name,
			}, []string{"asg_name", "region", "instance_id", "instance_type", "availability_zone", "prefix"})
			e.metricsMtx.Unlock()
		}
		var labels prometheus.Labels = map[string]string{
			"asg_name":          scr.AutoScalingGroup,
			"region":            scr.Region,
			"instance_id":       scr.InstanceId,
			"instance_type":     scr.InstanceType,
			"availability_zone": scr.AvailabilityZone,
			"prefix":            e.metricsName,
		}
		e.instanceMetrics[name].With(labels).Set(float64(scr.Value))
	}
}

func (e *Exporter) scrapeAsg(sess *session.Session, groupScrapes chan<- GroupScrapeResult, instanceScrapes chan<- InstanceScrapeResult, asg *autoscaling.Group) error {
	log.WithField("autoScalingGroup", *asg.AutoScalingGroupName).Debug("getting metrics from the auto scaling group")

	var pendingInstances, inServiceInstances, standbyInstances, terminatingInstances, spotInstances int
	var instanceIds []*string

	if len(asg.Instances) > 0 {
		for _, inst := range asg.Instances {
			switch *inst.LifecycleState {
			case "InService":
				inServiceInstances++
			case "Pending":
				pendingInstances++
			case "Terminating":
				terminatingInstances++
			case "Standby":
				standbyInstances++
			}
			instanceIds = append(instanceIds, inst.InstanceId)
		}
	}

	groupScrapes <- GroupScrapeResult{
		Name:             "instances_total",
		Value:            float64(len(asg.Instances)),
		AutoScalingGroup: *asg.AutoScalingGroupName,
		Region:           *sess.Config.Region,
		Prefix:           e.metricsName,
	}
	groupScrapes <- GroupScrapeResult{
		Name:             "pending_instances_total",
		Value:            float64(pendingInstances),
		AutoScalingGroup: *asg.AutoScalingGroupName,
		Region:           *sess.Config.Region,
		Prefix:           e.metricsName,
	}
	groupScrapes <- GroupScrapeResult{
		Name:             "inservice_instances_total",
		Value:            float64(inServiceInstances),
		AutoScalingGroup: *asg.AutoScalingGroupName,
		Region:           *sess.Config.Region,
		Prefix:           e.metricsName,
	}
	groupScrapes <- GroupScrapeResult{
		Name:             "terminating_instances_total",
		Value:            float64(terminatingInstances),
		AutoScalingGroup: *asg.AutoScalingGroupName,
		Region:           *sess.Config.Region,
		Prefix:           e.metricsName,
	}
	groupScrapes <- GroupScrapeResult{
		Name:             "standby_instances_total",
		Value:            float64(standbyInstances),
		AutoScalingGroup: *asg.AutoScalingGroupName,
		Region:           *sess.Config.Region,
		Prefix:           e.metricsName,
	}

	var countError *instanceScrapeError
	if len(instanceIds) > 0 {
		var err error
		log.WithField("autoScalingGroup", *asg.AutoScalingGroupName).Debug("getting metrics from the instances in the autoscaling group")
		spotInstances, err = e.scrapeInstances(sess, instanceScrapes, *asg.AutoScalingGroupName, instanceIds)
		if err != nil {
			if e, ok := err.(*instanceScrapeError); ok {
				countError = e
			} else {
				return err
			}
		}
	}

	groupScrapes <- GroupScrapeResult{
		Name:             "spot_instances_total",
		Value:            float64(spotInstances),
		AutoScalingGroup: *asg.AutoScalingGroupName,
		Region:           *sess.Config.Region,
		Prefix:           e.metricsName,
	}

	if countError != nil {
		return countError
	}
	return nil
}

func (e *Exporter) scrapeInstances(sess *session.Session, scrapes chan<- InstanceScrapeResult, asgName string, instanceIds []*string) (int, error) {
	var errorCount uint64
	ec2Svc := ec2.New(sess, aws.NewConfig())
	var spotRequests []*string

	err := ec2Svc.DescribeInstancesPages(&ec2.DescribeInstancesInput{
		InstanceIds: instanceIds,
	}, func(output *ec2.DescribeInstancesOutput, lastPage bool) bool {
		for _, reservation := range output.Reservations {
			for _, instance := range reservation.Instances {
				if instance.SpotInstanceRequestId != nil {
					spotRequests = append(spotRequests, instance.SpotInstanceRequestId)
				}
			}
		}
		return true
	})
	if err != nil {
		return 0, err
	}

	if len(spotRequests) > 0 {
		describeSpotInstanceRequestsOutput, err := ec2Svc.DescribeSpotInstanceRequests(&ec2.DescribeSpotInstanceRequestsInput{
			SpotInstanceRequestIds: spotRequests,
		})
		if err != nil {
			return 0, err
		}

		for _, spotRequest := range describeSpotInstanceRequestsOutput.SpotInstanceRequests {
			spotBidPrice, err := strconv.ParseFloat(*spotRequest.SpotPrice, 64)
			if err != nil {
				log.WithField("autoScalingGroup", asgName).Error(err)
				errorCount++
			} else {
				scrapes <- InstanceScrapeResult{
					Name:             "spot_bid_price",
					Value:            spotBidPrice,
					AutoScalingGroup: asgName,
					Region:           *sess.Config.Region,
					Prefix:           e.metricsName,
					InstanceId:       *spotRequest.InstanceId,
					AvailabilityZone: *spotRequest.LaunchedAvailabilityZone,
					InstanceType:     *spotRequest.LaunchSpecification.InstanceType,
				}
			}
		}
	}

	if errorCount > 0 {
		return len(spotRequests), &instanceScrapeError{errorCount}
	}
	return len(spotRequests), nil
}
