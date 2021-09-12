package advisor

import (
	//"fmt"
	"context"

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
	pods, err := client.CoreV1().Pods("default").List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}


	for _, pod := range pods.Items {
		queryPrometheusForPod(ctx, promClient, pod)
	}

	//fmt.Printf("%+v", pods)
	return nil
}
