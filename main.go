package main

import (
	"context"
	"fmt"
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

	fmt.Println("time,node_total,node_ready,node_tainted,node_deleting,nodeclaim_total,nodeclaim_launched,nodeclaim_registered,nodeclaim_initialized,nodeclaim_drifted,nodeclaim_disrupted,nodeclaim_deleting")
	for {
		nodeList := &corev1.NodeList{}
		if err := c.List(ctx, nodeList); err != nil {
			continue
		}
		nodeClaimList := &v1.NodeClaimList{}
		if err := c.List(ctx, nodeClaimList); err != nil {
			continue
		}
		var launchedCount, registeredCount, initializedCount, driftedCount, deletingCount, disruptedCount, nodeReadyCount, nodeDeletingCount, nodeTaintedCount atomic.Int64
		workqueue.ParallelizeUntil(ctx, 100, len(nodeClaimList.Items), func(i int) {
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
		workqueue.ParallelizeUntil(ctx, 100, len(nodeList.Items), func(i int) {
			if GetCondition(&nodeList.Items[i], corev1.NodeReady).Status == corev1.ConditionTrue {
				nodeReadyCount.Add(1)
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
		fmt.Printf("%s,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d\n", time.Now().Format(time.RFC3339), len(nodeList.Items), nodeReadyCount.Load(), nodeTaintedCount.Load(), nodeDeletingCount.Load(), len(nodeClaimList.Items), launchedCount.Load(), registeredCount.Load(), initializedCount.Load(), driftedCount.Load(), disruptedCount.Load(), deletingCount.Load())
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
