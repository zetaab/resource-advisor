package advisor

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	promv1 "github.com/prometheus/client_golang/api/prometheus/v1"
	prommodel "github.com/prometheus/common/model"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/api/core/v1"
	apiResource "k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	promOperatorClusterURL = "/api/v1/namespaces/monitoring/services/prometheus-operated:web/proxy/"
	podCPURequest          = `avg_over_time(node_namespace_pod_container:container_cpu_usage_seconds_total:sum_irate{pod="%s", container!=""}[1w])`
	podCPULimit            = `max_over_time(node_namespace_pod_container:container_cpu_usage_seconds_total:sum_irate{pod="%s", container!=""}[1w]) * 1.2`
	podMemoryRequest       = `avg_over_time(container_memory_working_set_bytes{pod="%s", container!=""}[1w])`
	podMemoryLimit         = `(max_over_time(container_memory_working_set_bytes{pod="%s", container!=""}[1w])) * 1.2`
	deploymentRevision     = "deployment.kubernetes.io/revision"
)

func findConfig() (*rest.Config, error) {
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(clientcmd.NewDefaultClientConfigLoadingRules(), &clientcmd.ConfigOverrides{}).ClientConfig()
}

func newClientSet() (*kubernetes.Clientset, error) {
	config, err := findConfig()
	if err != nil {
		return nil, err
	}
	return kubernetes.NewForConfig(config)
}

func getCurrentValue(quantityMap v1.ResourceRequirements) podResources {
	resources := podResources{}
	if val, ok := quantityMap.Requests[v1.ResourceCPU]; ok {
		currentValue := &val
		asApproximateFloat64 := currentValue.AsApproximateFloat64()
		resources.RequestCPU = &asApproximateFloat64
	}
	if val, ok := quantityMap.Limits[v1.ResourceCPU]; ok {
		currentValue := &val
		asApproximateFloat64 := currentValue.AsApproximateFloat64()
		resources.LimitCPU = &asApproximateFloat64
	}
	if val, ok := quantityMap.Requests[v1.ResourceMemory]; ok {
		currentValue := &val
		asApproximateFloat64 := currentValue.AsApproximateFloat64()
		resources.RequestMem = &asApproximateFloat64
	}
	if val, ok := quantityMap.Limits[v1.ResourceMemory]; ok {
		currentValue := &val
		asApproximateFloat64 := currentValue.AsApproximateFloat64()
		resources.LimitMem = &asApproximateFloat64
	}
	return resources
}

func queryStatistic(ctx context.Context, client *promClient, request string, now time.Time) (map[string]prommodel.SampleValue, error) {
	output := make(map[string]prommodel.SampleValue)
	response, _, err := queryPrometheus(ctx, client, request, now)
	if err != nil {
		return output, fmt.Errorf("Error querying statistic %v", err)
	}
	asSamples := response.(prommodel.Vector)

	sampleArray := []*prommodel.Sample{}
	for _, sample := range asSamples {
		sampleArray = append(sampleArray, sample)
	}

	for _, item := range sampleArray {
		containerName := ""
		for k, v := range item.Metric {
			if k == "container" {
				containerName = string(v)
				break
			}
		}
		output[containerName] = item.Value
	}

	return output, nil
}

func makeSuggestion(output []suggestion, podName string, containerName string, text string, currrentUsage prommodel.SampleValue, currentResource *float64, mode int) []suggestion {
	usage := float64(currrentUsage)
	resource := asFloat(currentResource)

	// if usage is >20% lower
	if usage < resource && (usage*100/resource) < 80 {
		output = append(output, suggestion{
			Pod:       podName,
			Container: containerName,
			Message:   fmt.Sprintf("Decrease %s", text),
			OldValue:  resource,
			NewValue:  usage,
		})
	}

	// if usage is >10% higher
	if usage > resource && (usage*100/resource) > 110 {
		output = append(output, suggestion{
			Pod:       podName,
			Container: containerName,
			Message:   fmt.Sprintf("Increase %s", text),
			OldValue:  resource,
			NewValue:  usage,
		})
	}
	return output
}

func queryPrometheusForPod(ctx context.Context, client *promClient, pod v1.Pod) ([]suggestion, error) {
	now := time.Now()

	suggestions := []suggestion{}

	podCPURequests, err := queryStatistic(ctx, client, fmt.Sprintf(podCPURequest, pod.Name), now)
	if err != nil {
		return nil, err
	}

	podCPULimits, err := queryStatistic(ctx, client, fmt.Sprintf(podCPULimit, pod.Name), now)
	if err != nil {
		return nil, err
	}

	podMemRequests, err := queryStatistic(ctx, client, fmt.Sprintf(podMemoryRequest, pod.Name), now)
	if err != nil {
		return nil, err
	}

	podMemLimits, err := queryStatistic(ctx, client, fmt.Sprintf(podMemoryLimit, pod.Name), now)
	if err != nil {
		return nil, err
	}

	for _, container := range pod.Spec.Containers {
		currentResources := getCurrentValue(container.Resources)
		if currentResources.RequestCPU == nil {
			suggestions = append(suggestions, suggestion{
				Pod:       pod.Name,
				Container: container.Name,
				Message:   "Define CPU Requests",
			})
		} else {
			val, ok := podCPURequests[container.Name]
			if ok {
				suggestions = makeSuggestion(suggestions, pod.Name, container.Name, "CPU Requests", val, currentResources.RequestCPU, 0)
			} else {
				suggestions = append(suggestions, suggestion{
					Pod:       pod.Name,
					Container: container.Name,
					Message:   "Could not find CPU Requests from prometheus",
				})
			}
		}

		if currentResources.RequestMem == nil {
			suggestions = append(suggestions, suggestion{
				Pod:       pod.Name,
				Container: container.Name,
				Message:   "Define Memory Requests",
			})
		} else {
			val, ok := podMemRequests[container.Name]
			if ok {
				suggestions = makeSuggestion(suggestions, pod.Name, container.Name, "Memory Requests", val, currentResources.RequestMem, 1)
			} else {
				suggestions = append(suggestions, suggestion{
					Pod:       pod.Name,
					Container: container.Name,
					Message:   "Could not find Memory Requests from prometheus",
				})
			}
		}

		if currentResources.LimitCPU == nil {
			suggestions = append(suggestions, suggestion{
				Pod:       pod.Name,
				Container: container.Name,
				Message:   "Define CPU Limits",
			})
		} else {
			val, ok := podCPULimits[container.Name]
			if ok {
				suggestions = makeSuggestion(suggestions, pod.Name, container.Name, "CPU Limits", val, currentResources.LimitCPU, 0)
			} else {
				suggestions = append(suggestions, suggestion{
					Pod:       pod.Name,
					Container: container.Name,
					Message:   "Could not find CPU Limits from prometheus",
				})
			}
		}

		if currentResources.LimitMem == nil {
			suggestions = append(suggestions, suggestion{
				Pod:       pod.Name,
				Container: container.Name,
				Message:   "Define Memory Limits",
			})
		} else {
			val, ok := podMemLimits[container.Name]
			if ok {
				suggestions = makeSuggestion(suggestions, pod.Name, container.Name, "Memory Limits", val, currentResources.LimitMem, 1)
			} else {
				suggestions = append(suggestions, suggestion{
					Pod:       pod.Name,
					Container: container.Name,
					Message:   "Could not find Memory Limits from prometheus",
				})
			}
		}
	}
	return suggestions, nil
}

func asFloat(val *float64) float64 {
	if val == nil {
		return 0.00
	}
	return *val
}

func asPointer(input apiResource.Quantity) *apiResource.Quantity {
	return &input
}

func findReplicaset(replicasets *appsv1.ReplicaSetList, generation int64) (*appsv1.ReplicaSet, error) {
	for _, replicaset := range replicasets.Items {
		val, ok := replicaset.Annotations[deploymentRevision]
		if ok && val == strconv.FormatInt(generation, 10) {
			return &replicaset, nil
		}
	}
	return nil, fmt.Errorf("could not find replicaset")
}

func makePrometheusClientForCluster() (*promClient, error) {
	config, err := findConfig()
	if err != nil {
		return nil, err
	}

	promurl := fmt.Sprintf("%s%s", config.Host, promOperatorClusterURL)
	tlsConf := config.TLSClientConfig
	cert, err := tls.X509KeyPair(tlsConf.CertData, tlsConf.KeyData)
	if err != nil {
		return nil, err
	}

	caCertPool := x509.NewCertPool()
	caCertPool.AppendCertsFromPEM(tlsConf.CAData)

	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      caCertPool,
	}
	tlsConfig.BuildNameToCertificate()
	transport := &http.Transport{TLSClientConfig: tlsConfig}
	client := http.Client{Transport: transport}

	u, err := url.Parse(promurl)
	if err != nil {
		return nil, err
	}
	u.Path = strings.TrimRight(u.Path, "/")

	return &promClient{
		endpoint: u,
		client:   client,
	}, nil
	return nil, nil
}

func queryPrometheus(ctx context.Context, client *promClient, query string, ts time.Time) (interface{}, promv1.Warnings, error) {
	promcli := promv1.NewAPI(client)
	return promcli.Query(ctx, query, ts)
}

func queryRangePrometheus(ctx context.Context, client *promClient, r promv1.Range, query string) (prommodel.Value, promv1.Warnings, error) {
	promcli := promv1.NewAPI(client)
	return promcli.QueryRange(ctx, query, r)
}

func (c *promClient) URL(ep string, args map[string]string) *url.URL {
	p := path.Join(c.endpoint.Path, ep)

	for arg, val := range args {
		arg = ":" + arg
		p = strings.Replace(p, arg, val, -1)
	}

	u := *c.endpoint
	u.Path = p

	return &u
}

func (c *promClient) Do(ctx context.Context, req *http.Request) (*http.Response, []byte, error) {
	if ctx != nil {
		req = req.WithContext(ctx)
	}
	resp, err := c.client.Do(req)
	defer func() {
		if resp != nil {
			resp.Body.Close()
		}
	}()

	if err != nil {
		return nil, nil, err
	}

	var body []byte
	done := make(chan struct{})
	go func() {
		body, err = ioutil.ReadAll(resp.Body)
		close(done)
	}()

	select {
	case <-ctx.Done():
		<-done
		err = resp.Body.Close()
		if err == nil {
			err = ctx.Err()
		}
	case <-done:
	}

	return resp, body, err
}
