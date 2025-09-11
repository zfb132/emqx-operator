package api

import (
	"fmt"
	"net/http"

	emperror "emperror.dev/errors"
	req "github.com/emqx/emqx-operator/internal/requester"
)

type apiError struct {
	StatusCode int
	Message    string
}

var (
	ErrorNotFound           = apiError{StatusCode: 404}
	ErrorServiceUnavailable = apiError{StatusCode: 503}
)

func (e apiError) Error() string {
	return fmt.Sprintf("HTTP %d, response: %s", e.StatusCode, e.Message)
}

func (e apiError) Is(target error) bool {
	if target, ok := target.(apiError); ok {
		return e.StatusCode == target.StatusCode
	}
	return false
}

func get(req req.RequesterInterface, path string) ([]byte, error) {
	return request(req, "GET", path, nil, nil)
}

func post(req req.RequesterInterface, path string, body []byte) ([]byte, error) {
	return request(req, "POST", path, body, nil)
}

func request(req req.RequesterInterface, method string, path string, body []byte, header http.Header) ([]byte, error) {
	url := req.GetURL(path)
	resp, body, err := req.Request(method, url, body, header)
	if err != nil {
		return nil, emperror.Wrapf(err, "error accessing EMQX API %s", url.String())
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		err := apiError{StatusCode: resp.StatusCode, Message: string(body)}
		return nil, emperror.Wrapf(err, "error accessing EMQX API %s", url.String())
	}
	return body, nil
}
