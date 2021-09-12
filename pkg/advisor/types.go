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
	oldValue interface{}
	newValue interface{}
}
