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
	apresource "k8s.io/apimachinery/pkg/api/resource"
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

	totalCPUSave := float64(0.00)
	totalMemSave := float64(0.00)
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

			cpuSave := float64(0.00)
			memSave := float64(0.00)
			data, cpuSave, memSave = o.analyzeDeployment(data, namespace, deployment, final)
			totalCPUSave += cpuSave
			totalMemSave += memSave
		}
	}

	table := tablewriter.NewWriter(os.Stdout)
	table.SetHeader([]string{"Namespace", "Deployment", "Container", "Request CPU (spec)", "Request MEM (spec)", "Limit CPU (spec)", "Limit MEM (spec)"})
	for _, v := range data {
		table.Append(v)
	}
	table.Render()

	fmt.Printf("Total savings:\n")

	totalMem := int64(totalMemSave)
	totalMemStr := ByteCountSI(totalMem)
	if totalMem < 0 {
		totalMem *= -1
		totalMemStr = ByteCountSI(totalMem)
		totalMemStr = fmt.Sprintf("-%s", totalMemStr)
	}
	fmt.Printf("You could save %.2f vCPUs and %s Memory by changing the settings\n", totalCPUSave, totalMemStr)

	return nil
}

func currentValue(resources v1.ResourceRequirements, method string, resource v1.ResourceName, current int, format apresource.Format) (float64, string) {
	curSaving := float64(float64(current) * 1000 * 1000)
	if format == apresource.DecimalSI {
		curSaving = float64(float64(current) / 1000)
	}

	if method == "limit" {
		val, ok := resources.Limits[resource]
		if ok {
			return val.AsApproximateFloat64() - curSaving, val.String()
		}
	} else {
		val, ok := resources.Requests[resource]
		if ok {
			return val.AsApproximateFloat64() - curSaving, val.String()
		}
	}
	return -1*curSaving, "<nil>"
}

func (o *Options) analyzeDeployment(data [][]string, namespace string, deployment appsv1.Deployment, finalMetrics prometheusMetrics) ([][]string, float64, float64) {
	totalCPUSavings := float64(0.00)
	totalMemSavings := float64(0.00)
	for _, container := range deployment.Spec.Template.Spec.Containers {
		reqCpu := int(finalMetrics.RequestCPU[container.Name] * 1000)
		reqMem := int(finalMetrics.RequestMem[container.Name])
		limCpu := int(finalMetrics.LimitCPU[container.Name] * 1000)
		limMem := int(finalMetrics.LimitMem[container.Name])

		reqCpuSave, strReqCPU := currentValue(container.Resources, "request", v1.ResourceCPU, reqCpu, apresource.DecimalSI)
		reqMemSave, strReqMem := currentValue(container.Resources, "request", v1.ResourceMemory, reqMem, apresource.BinarySI)
		_, strLimCPU := currentValue(container.Resources, "limit", v1.ResourceCPU, limCpu, apresource.DecimalSI)
		_, strLimMem := currentValue(container.Resources, "limit", v1.ResourceMemory, limMem, apresource.BinarySI)

		totalCPUSavings += reqCpuSave * float64(*deployment.Spec.Replicas)
		totalMemSavings += reqMemSave * float64(*deployment.Spec.Replicas)
		data = append(data, []string{
			namespace,
			deployment.Name,
			container.Name,
			fmt.Sprintf("%dm (%s)", reqCpu, strReqCPU),
			fmt.Sprintf("%dMi (%s)", reqMem, strReqMem),
			fmt.Sprintf("%dm (%s)", limCpu, strLimCPU),
			fmt.Sprintf("%dMi (%s)", limMem, strLimMem),
		})
	}
	return data, totalCPUSavings, totalMemSavings
}
