package advisor

import (
	"context"
	"fmt"
	"math"
	"os"

	"github.com/olekukonko/tablewriter"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func Run(o *Options) error {
	client, err := newClientSet()
	if err != nil {
		return err
	}

	promClient, err := makePrometheusClientForCluster()
	if err != nil {
		return err
	}

	ctx := context.Background()
	deployments, err := client.AppsV1().Deployments("default").List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}

	data := [][]string{}
	for _, deployment := range deployments.Items {
		selector, err := metav1.LabelSelectorAsSelector(deployment.Spec.Selector)
		if err != nil {
			return err
		}

		replicasets, err := client.AppsV1().ReplicaSets("default").List(ctx, metav1.ListOptions{
			LabelSelector: selector.String(),
		})
		if err != nil {
			return err
		}

		replicaset, err := findReplicaset(replicasets, deployment)
		if err != nil {
			return err
		}

		selector, err = metav1.LabelSelectorAsSelector(replicaset.Spec.Selector)
		if err != nil {
			return err
		}

		pods, err := client.CoreV1().Pods("default").List(ctx, metav1.ListOptions{
			LabelSelector: selector.String(),
		})
		if err != nil {
			return err
		}

		totalLimitCPU := make(map[string][]float64)
		totalLimitMem := make(map[string][]float64)
		totalRequestCPU := make(map[string][]float64)
		totalRequestMem := make(map[string][]float64)

		for _, pod := range pods.Items {
			output, err := queryPrometheusForPod(ctx, promClient, pod)
			if err != nil {
				return err
			}
			for k, v := range output.RequestCPU {
				totalRequestCPU[k] = append(totalRequestCPU[k], v)
			}
			for k, v := range output.RequestMem {
				totalRequestMem[k] = append(totalRequestMem[k], v)
			}
			for k, v := range output.LimitCPU {
				totalLimitCPU[k] = append(totalLimitCPU[k], v)
			}
			for k, v := range output.LimitMem {
				totalLimitMem[k] = append(totalLimitMem[k], v)
			}
		}
		final := prometheusMetrics{
			LimitCPU:   make(map[string]float64),
			LimitMem:   make(map[string]float64),
			RequestCPU: make(map[string]float64),
			RequestMem: make(map[string]float64),
		}
		for k, v := range totalRequestCPU {
			final.RequestCPU[k] = math.Ceil(float64Average(v)*10) / 10
		}
		for k, v := range totalRequestMem {
			final.RequestMem[k] = math.Ceil(float64Average(v)/100) * 100
		}
		for k, v := range totalLimitCPU {
			final.LimitCPU[k] = math.Ceil(float64Average(v)*10) / 10
		}
		for k, v := range totalLimitMem {
			final.LimitMem[k] = math.Ceil(float64Average(v)/100) * 100
		}

		data = analyzeDeployment(data, deployment, final)
	}

	table := tablewriter.NewWriter(os.Stdout)
	table.SetHeader([]string{"Namespace", "Deployment", "Container", "Request CPU", "Request MEM", "Limit CPU", "Limit MEM"})
	for _, v := range data {
		table.Append(v)
	}
	table.Render()

	return nil
}

func analyzeDeployment(data [][]string, deployment appsv1.Deployment, finalMetrics prometheusMetrics) [][]string {
	for _, container := range deployment.Spec.Template.Spec.Containers {
		reqCpu, _ := finalMetrics.RequestCPU[container.Name]
		reqMem, _ := finalMetrics.RequestMem[container.Name]
		limCpu, _ := finalMetrics.LimitCPU[container.Name]
		limMem, _ := finalMetrics.LimitMem[container.Name]
		data = append(data, []string{
			"default",
			deployment.Name,
			container.Name,
			fmt.Sprintf("%dm", int(reqCpu*1000)),
			fmt.Sprintf("%dMi", int(reqMem)),
			fmt.Sprintf("%dm", int(limCpu*1000)),
			fmt.Sprintf("%dMi", int(limMem)),
		})
	}
	return data
}
