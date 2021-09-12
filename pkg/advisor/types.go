package advisor

import (
	"net/http"
	"net/url"
)

type Options struct {
}

type promClient struct {
	endpoint *url.URL
	client   http.Client
}

type suggestion struct {
	OldValue  float64
	NewValue  float64
	Pod       string
	Container string
	Message   string
}

type podResources struct {
	LimitCPU   *float64
	LimitMem   *float64
	RequestCPU *float64
	RequestMem *float64
}
