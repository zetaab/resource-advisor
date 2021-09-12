package advisor

import (
	"context"
	"fmt"
	"math"
	"os"
	"strings"

	"github.com/olekukonko/tablewriter"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/api/core/v1"
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

	if o.NamespaceInput == "" {
		_, namespace, err := findConfig()
		if err != nil {
			return err
		}
		o.Namespaces = namespace
	} else {
		o.Namespaces = o.NamespaceInput
	}

	ctx := context.Background()
	data := [][]string{}
	for _, namespace := range strings.Split(o.Namespaces, ",") {
		deployments, err := client.AppsV1().Deployments(namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			return err
		}

		for _, deployment := range deployments.Items {
			selector, err := metav1.LabelSelectorAsSelector(deployment.Spec.Selector)
			if err != nil {
				return err
			}

			replicasets, err := client.AppsV1().ReplicaSets(namespace).List(ctx, metav1.ListOptions{
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

			pods, err := client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
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
				output, err := o.queryPrometheusForPod(ctx, promClient, pod)
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
				scale := 10
				value := float64Average(v)
				final.RequestCPU[k] = math.Ceil(value*float64(scale)) / float64(scale)
			}
			for k, v := range totalRequestMem {
				final.RequestMem[k] = math.Ceil(float64Average(v)/100) * 100
			}
			for k, v := range totalLimitCPU {
				scale := 10
				value := float64Average(v)
				final.LimitCPU[k] = math.Ceil(value*float64(scale)) / float64(scale)
			}
			for k, v := range totalLimitMem {
				final.LimitMem[k] = math.Ceil(float64Average(v)/100) * 100
			}

			data = o.analyzeDeployment(data, namespace, deployment, final)
		}
	}

	table := tablewriter.NewWriter(os.Stdout)
	table.SetHeader([]string{"Namespace", "Deployment", "Container", "Request CPU (spec)", "Request MEM (spec)", "Limit CPU (spec)", "Limit MEM (spec)"})
	for _, v := range data {
		table.Append(v)
	}
	table.Render()

	return nil
}

func currentValue(resources v1.ResourceRequirements, method string, resource v1.ResourceName) string {
	if method == "limit" {
		val, ok := resources.Limits[resource]
		if ok {
			return val.String()
		}
	}
	return "<nil>"
}

func (o *Options) analyzeDeployment(data [][]string, namespace string, deployment appsv1.Deployment, finalMetrics prometheusMetrics) [][]string {
	for _, container := range deployment.Spec.Template.Spec.Containers {
		reqCpu, _ := finalMetrics.RequestCPU[container.Name]
		reqMem, _ := finalMetrics.RequestMem[container.Name]
		limCpu, _ := finalMetrics.LimitCPU[container.Name]
		limMem, _ := finalMetrics.LimitMem[container.Name]
		data = append(data, []string{
			namespace,
			deployment.Name,
			container.Name,
			fmt.Sprintf("%dm (%s)", int(reqCpu*1000), currentValue(container.Resources, "request", v1.ResourceCPU)),
			fmt.Sprintf("%dMi (%s)", int(reqMem), currentValue(container.Resources, "request", v1.ResourceMemory)),
			fmt.Sprintf("%dm (%s)", int(limCpu*1000), currentValue(container.Resources, "limit", v1.ResourceCPU)),
			fmt.Sprintf("%dMi (%s)", int(limMem), currentValue(container.Resources, "limit", v1.ResourceMemory)),
		})
	}
	return data
}
