package main

import (
	"context"
	"encoding/csv"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"sync/atomic"
	"time"

	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	v1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

func main() {
	ctx := context.Background()
	config := ctrl.GetConfigOrDie()
	config.QPS = 5000
	config.Burst = 5000
	cache := lo.Must(cache.New(config, cache.Options{}))
	go cache.Start(ctx) // TODO: Make sure that this doesn't leak, since this is a CLI tool, we don't care if we leak
	lo.Must0(cache.WaitForCacheSync(ctx))
	c := lo.Must(client.New(config, client.Options{Cache: &client.CacheOptions{Reader: cache}}))

	// Define flags
	outputFile := flag.String("o", "", "Output CSV file")
	overwrite := flag.Bool("f", false, "Force overwrite if file exists")
	flag.Parse()

	// Check if file exists
	_, err := os.Stat(*outputFile)
	fileExists := !os.IsNotExist(err)

	if fileExists && !*overwrite && lo.FromPtr(outputFile) != "" {
		log.Fatalf("File %s already exists. Use -f flag to force overwrite", *outputFile)
	}

	file := &os.File{}
	var multiWriter io.Writer
	if lo.FromPtr(outputFile) != "" {
		file, err = os.Create(lo.FromPtr(outputFile))
		if err != nil {
			fmt.Println(err)
			return
		}
		multiWriter = io.MultiWriter(
			file,      // Write to file
			os.Stdout, // Write to standard output
		)
	} else {
		multiWriter = io.MultiWriter(
			os.Stdout, // Write to standard output
		)
	}
	csvWriter := csv.NewWriter(multiWriter)
	defer file.Close()

	head := []string{"time", "node_total", "node_ready", "node_unhealthy", "node_tainted", "node_deleting", "nodeclaim_total", "nodeclaim_launched", "nodeclaim_registered", "nodeclaim_initialized", "nodeclaim_drifted", "nodeclaim_disrupted", "nodeclaim_deleting"}
	err = csvWriter.Write(head)
	if err != nil {
		panic(err)
	}
	csvWriter.Flush()
	for {
		nodeList := &corev1.NodeList{}
		if err := c.List(ctx, nodeList); err != nil {
			continue
		}
		nodeClaimList := &v1.NodeClaimList{}
		if err := c.List(ctx, nodeClaimList); err != nil {
			continue
		}
		var launchedCount, registeredCount, initializedCount, driftedCount, deletingCount, disruptedCount, nodeReadyCount, nodeUnhealthyCount, nodeDeletingCount, nodeTaintedCount atomic.Int64
		workqueue.ParallelizeUntil(ctx, 1000, len(nodeClaimList.Items), func(i int) {
			if nodeClaimList.Items[i].StatusConditions().Get(v1.ConditionTypeLaunched).IsTrue() {
				launchedCount.Add(1)
			}
			if nodeClaimList.Items[i].StatusConditions().Get(v1.ConditionTypeRegistered).IsTrue() {
				registeredCount.Add(1)
			}
			if nodeClaimList.Items[i].StatusConditions().Get(v1.ConditionTypeInitialized).IsTrue() {
				initializedCount.Add(1)
			}
			if nodeClaimList.Items[i].StatusConditions().Get(v1.ConditionTypeDrifted).IsTrue() {
				driftedCount.Add(1)
			}
			if nodeClaimList.Items[i].StatusConditions().Get(v1.ConditionTypeDisruptionReason).IsTrue() {
				disruptedCount.Add(1)
			}
			if !nodeClaimList.Items[i].DeletionTimestamp.IsZero() {
				deletingCount.Add(1)
			}
		})
		workqueue.ParallelizeUntil(ctx, 1000, len(nodeList.Items), func(i int) {
			if GetCondition(&nodeList.Items[i], corev1.NodeReady).Status == corev1.ConditionTrue {
				nodeReadyCount.Add(1)
			}
			if GetCondition(&nodeList.Items[i], corev1.NodeConditionType("TestTypeReady")).Status == corev1.ConditionFalse {
				nodeUnhealthyCount.Add(1)
			}
			if !nodeList.Items[i].DeletionTimestamp.IsZero() {
				nodeDeletingCount.Add(1)
			}
			if lo.ContainsBy(nodeList.Items[i].Spec.Taints, func(t corev1.Taint) bool {
				return t.Key == "karpenter.sh/disrupted"
			}) {
				nodeTaintedCount.Add(1)
			}
		})
		record := []string{time.Now().Format(time.TimeOnly), fmt.Sprint(len(nodeList.Items)), fmt.Sprint(nodeReadyCount.Load()), fmt.Sprint(nodeUnhealthyCount.Load()), fmt.Sprint(nodeTaintedCount.Load()), fmt.Sprint(nodeDeletingCount.Load()), fmt.Sprint(len(nodeClaimList.Items)), fmt.Sprint(launchedCount.Load()), fmt.Sprint(registeredCount.Load()), fmt.Sprint(initializedCount.Load()), fmt.Sprint(driftedCount.Load()), fmt.Sprint(disruptedCount.Load()), fmt.Sprint(deletingCount.Load())}
		err = csvWriter.Write(record)
		if err != nil {
			panic(err)
		}
		csvWriter.Flush()
		time.Sleep(time.Second * 5)
	}
}

func GetCondition(n *corev1.Node, match corev1.NodeConditionType) corev1.NodeCondition {
	for _, condition := range n.Status.Conditions {
		if condition.Type == match {
			return condition
		}
	}
	return corev1.NodeCondition{}
}
