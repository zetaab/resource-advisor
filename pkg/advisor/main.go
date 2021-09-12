package advisor

import (
	"context"
	"fmt"

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

		replicaset, err := findReplicaset(replicasets, deployment.Generation)
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
		for _, pod := range pods.Items {
			output, err := queryPrometheusForPod(ctx, promClient, pod)
			if err != nil {
				return err
			}
			fmt.Printf("%+v\n", output)
		}

		break
	}

	//replicaSets := make(map[string][]suggestion)

	/*
		for _, pod := range pods.Items {
			output, err := queryPrometheusForPod(ctx, promClient, pod)
			if err != nil {
				return err
			}

			for _, owner := range pod.OwnerReferences {
				fmt.Printf("%+v\n", owner)
				if owner.Kind == "ReplicaSet" {
					replicaSets[owner.Name] = output
				}
			}
		}*/

	//fmt.Printf("%+v", pods)
	return nil
}
