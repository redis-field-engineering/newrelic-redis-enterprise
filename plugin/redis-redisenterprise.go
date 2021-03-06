package main

import (
	"fmt"
	"reflect"
	"time"

	"github.com/Redis-Field-Engineering/newrelic-redis-enterprise/plugin/utils"
	"github.com/leekchan/timeutil"

	sdkArgs "github.com/newrelic/infra-integrations-sdk/args"
	"github.com/newrelic/infra-integrations-sdk/data/metric"
	"github.com/newrelic/infra-integrations-sdk/integration"
)

type argumentList struct {
	sdkArgs.DefaultArgumentList
	Hostname  string `default:"localhost" help:"Hostname or IP where Redis server is running."`
	Port      int    `default:"9443" help:"Port on which Redis server is listening."`
	Username  string `default:"admin@example.com" help:"Username to login as."`
	Password  string `default:"myPass" help:"Password for login."`
	Eventtime int    `default:"60" help:"Interval for scraping events."`
}

const (
	integrationName    = "com.redis.redisenterprise"
	integrationVersion = "0.1.0"
)

var (
	args argumentList
)

func main() {
	// Create Integration
	i, err := integration.New(integrationName, integrationVersion, integration.Args(&args))
	panicOnErr(err)

	bdbEnts := make(map[int]*integration.Entity)

	// Fetch the list of Redis databases
	conf := &utils.RLConf{
		Hostname: args.Hostname,
		Port:     args.Port,
		User:     args.Username,
		Pass:     args.Password,
	}

	// Check to see if we are connecting to the cluster leader if not just exit
	// This is useful when running the integration all cluster nodes as only the master will submit metrics/events

	redir, err := utils.APIisRedirect(conf, "/v1/cluster")
	if err != nil || redir {
		return
	}

	// Get the cluster configuration
	cluster, err := utils.GetCluster(conf)
	panicOnErr(err)

	// Get the license information
	license, err := utils.GetLicense(conf)
	panicOnErr(err)

	// Get node information
	nodes, err := utils.GetNodes(conf)
	panicOnErr(err)

	// Get the list of Redis databases
	bdbs, err := utils.GetBDBs(conf)
	panicOnErr(err)

	// Grab the Redis DB stats
	bdbStats, err := utils.GetBDBStats(conf)
	panicOnErr(err)

	// Create Entity, entities name must be unique
	e1, err := i.Entity(cluster.Name, "redisecluster")
	panicOnErr(err)

	for _, val := range bdbs {
		s := fmt.Sprintf("%s:%s", cluster.Name, val.DBName)
		bdbEnts[val.Uid], err = i.Entity(s, "redisedb")
		panicOnErr(err)
	}

	// Add Inventory
	if args.All() || args.Inventory {
		err := e1.SetInventoryItem("RedisEnterpriseType", "value", "cluster")
		panicOnErr(err)
		for _, x := range bdbEnts {
			err := x.SetInventoryItem("RedisEnterpriseType", "value", "database")
			panicOnErr(err)
		}
	}
	// Add Metric
	if args.All() || args.Metrics {
		ms := e1.NewMetricSet("RedisEnterprise")
		clusterTotalShards := 0
		clusterTotalMemoryUsage := 0.0
		clusterTotalOps := 0.0
		err = ms.SetMetric("cluster.DaysUntilExpiration", float64(license.DaysUntilExpiration), metric.GAUGE)
		panicOnErr(err)
		err = ms.SetMetric("cluster.ShardsLicense", float64(license.ShardsLimit), metric.GAUGE)
		panicOnErr(err)
		err = ms.SetMetric("cluster.ClusterTotalMemory", float64(nodes.NodeMemory), metric.GAUGE)
		panicOnErr(err)
		err = ms.SetMetric("cluster.ClusterTotalCores", float64(nodes.NodeCores), metric.GAUGE)
		panicOnErr(err)
		err = ms.SetMetric("cluster.ClusterActiveNodes", float64(nodes.ActiveNodes), metric.GAUGE)
		panicOnErr(err)
		err = ms.SetMetric("cluster.ClusterNodes", float64(nodes.NodeCount), metric.GAUGE)
		panicOnErr(err)
		for _, val := range bdbs {
			// Setup the list of metrics we want to submit
			bdb_stats_list := []string{
				"AvgLatency", "AvgReadLatency", "AvgWriteLatency", "Conns", "EgressBytes", "EvictedObjects",
				"ExpiredObjects", "IngressBytes", "OtherReq", "ReadHits", "ReadMisses", "ReadReq",
				"ShardCPUSystem", "ShardCPUUser", "TotalReq", "UsedMemory", "WriteHits", "WriteMisses", "WriteReq"}

			// If the DB has RoF enabled, add the RoF metrics otherwise skip
			if val.Bigstore {
				bdb_stats_list = append(bdb_stats_list, []string{
					"BigstoreObjsRam", "BigstoreObjsFlash", "BigstoreIoReads",
					"BigstoreIoWrites", "BigstoreThroughput", "BigWriteRam",
					"BigWriteFlash", "BigDelRam", "BigDelFlash"}...)
			}

			bdbMs := bdbEnts[val.Uid].NewMetricSet("RedisEnterprise")
			err = bdbMs.SetMetric("bdb.ShardCount", float64(val.ShardsUsed), metric.GAUGE)
			panicOnErr(err)
			err = bdbMs.SetMetric("bdb.Endpoints", float64(val.Endpoints), metric.GAUGE)
			panicOnErr(err)
			err = bdbMs.SetMetric("bdb.MemoryLimit", float64(val.Limit), metric.GAUGE)
			panicOnErr(err)
			//// Grab all bdb Gauges
			for _, x := range bdb_stats_list {
				s := reflect.ValueOf(bdbStats[val.Uid]).FieldByName(x).Interface().(float64)
				err = bdbMs.SetMetric(fmt.Sprintf("bdb.%s", x), float64(s), metric.GAUGE)
				panicOnErr(err)
			}

			// Derived metrics
			err = bdbMs.SetMetric(
				"bdb.UsedMemoryPercent",
				float64(100*(float64(bdbStats[val.Uid].UsedMemory)/float64(val.Limit))),
				metric.GAUGE,
			)
			panicOnErr(err)

			// Update the total cluster metrics from each BDB
			clusterTotalShards += val.ShardsUsed
			clusterTotalMemoryUsage += float64(bdbStats[val.Uid].UsedMemory)
			clusterTotalOps += float64(bdbStats[val.Uid].TotalReq)

			if val.Crdt {
				err = bdbMs.SetMetric("bdb.CrdtSyncStatus", float64(val.SyncStatus), metric.GAUGE)
				panicOnErr(err)
				st := time.Now().Add(time.Duration(-args.Eventtime) * time.Second)
				params := map[string]string{
					"stime":    timeutil.Strftime(&st, "%Y-%m-%dT%H:%M:%SZ"),
					"interval": "10sec",
				}
				stats, err := utils.GetCrdt(conf, val.Uid, params)
				panicOnErr(err)
				err = bdbMs.SetMetric("crdt.CrdtEgressBytes", float64(stats.CrdtEgressBytes), metric.GAUGE)
				panicOnErr(err)
				err = bdbMs.SetMetric("crdt.CrdtEgressBytesDecompressed", float64(stats.CrdtEgressBytesDecompressed), metric.GAUGE)
				panicOnErr(err)
				err = bdbMs.SetMetric("crdt.CrdtIngressBytes", float64(stats.CrdtIngressBytes), metric.GAUGE)
				panicOnErr(err)
				err = bdbMs.SetMetric("crdt.CrdtIngressBytesDecompressed", float64(stats.CrdtIngressBytesDecompressed), metric.GAUGE)
				panicOnErr(err)
				err = bdbMs.SetMetric("crdt.CrdtPendingLocalWritesMax", float64(stats.CrdtPendingLocalWritesMax), metric.GAUGE)
				panicOnErr(err)
				err = bdbMs.SetMetric("crdt.CrdtPendingLocalWritesMin", float64(stats.CrdtPendingLocalWritesMin), metric.GAUGE)
				panicOnErr(err)
				err = bdbMs.SetMetric("crdt.CrdtLocalIngressLagTime", float64(stats.CrdtLocalIngressLagTime), metric.GAUGE)
				panicOnErr(err)

			}
		}

		// Set the top level cluster aggregated metrics
		err = ms.SetMetric("cluster.TotalShardsUsed", float64(clusterTotalShards), metric.GAUGE)
		panicOnErr(err)
		err = ms.SetMetric("cluster.TotalMemoryUsed", float64(clusterTotalMemoryUsage), metric.GAUGE)
		panicOnErr(err)
		err = ms.SetMetric(
			"cluster.TotalMemoryUsedPercent",
			float64(100*float64(clusterTotalMemoryUsage)/float64(nodes.NodeMemory)),
			metric.GAUGE,
		)
		panicOnErr(err)
		err = ms.SetMetric("cluster.TotalReqs", float64(clusterTotalOps), metric.GAUGE)
		panicOnErr(err)
	}

	panicOnErr(i.Publish())
}

func panicOnErr(err error) {
	if err != nil {
		panic(err)
	}
}
