package k8s

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"strconv"
	"strings"
	"text/template"

	"github.com/flanksource/kommons"
	"github.com/flanksource/kommons/ktemplate"
	templatev1 "github.com/flanksource/template-operator/api/v1"
	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/kubernetes"
)

type RESTManager struct {
	Client *kommons.Client
	kubernetes.Interface
	Log     logr.Logger
	FuncMap template.FuncMap
}

func NewRESTManager(c *kommons.Client, log logr.Logger) (*RESTManager, error) {
	clientset, _ := c.GetClientset()

	functions := ktemplate.NewFunctions(clientset)

	tm := &RESTManager{
		Client:    c,
		Interface: clientset,
		Log:       log,
		FuncMap:   functions.FuncMap(),
	}
	return tm, nil
}

func (r *RESTManager) Update(ctx context.Context, rest *templatev1.REST) error {
	if sameGeneration(rest) {
		return nil
	}

	url := rest.Spec.Update.URL
	method := rest.Spec.Update.Method
	body := rest.Spec.Update.Body

	resp, err := r.doRequest(ctx, rest, url, method, body)
	if err != nil {
		return errors.Wrap(err, "failed to send request")
	}

	fmt.Printf("Resp body: %s\n", string(resp))

	respBody := map[string]interface{}{}
	if err := json.Unmarshal(resp, &respBody); err != nil {
		r.Log.Info("failed to unmarshal response body", "error", err)
	}

	if rest.Spec.Update.Status != nil {
		for k, v := range rest.Spec.Update.Status {
			value, err := r.templateStatus(rest, respBody, v)
			if err != nil {
				return errors.Wrapf(err, "failed to template status field %s", k)
			}
			rest.Status[k] = value
		}
	}

	rest.Status["observedGeneration"] = strconv.FormatInt(rest.ObjectMeta.Generation, 10)

	return nil
}

func (r *RESTManager) Delete(ctx context.Context, rest *templatev1.REST) error {
	url := rest.Spec.Remove.URL
	method := rest.Spec.Remove.Method
	body := rest.Spec.Remove.Body

	_, err := r.doRequest(ctx, rest, url, method, body)
	if err != nil {
		return errors.Wrap(err, "failed to send request")
	}

	fmt.Printf("Received delete request\n")
	return nil
}

func (r *RESTManager) doRequest(ctx context.Context, rest *templatev1.REST, url, method, body string) ([]byte, error) {
	newBody, err := r.templateField(rest, body)
	if err != nil {
		return nil, errors.Wrap(err, "failed to template body")
	}

	newURL, err := r.templateField(rest, url)
	if err != nil {
		return nil, errors.Wrap(err, "failed to template url")
	}
	if newURL == "" {
		if rest.Spec.URL == "" {
			return nil, errors.Wrap(err, "url cannot be empty")
		}
		newURL = rest.Spec.URL
	}

	client := &http.Client{}

	// set the HTTP method, url, and request body
	req, err := http.NewRequest(method, newURL, bytes.NewBuffer([]byte(newBody)))
	if err != nil {
		return nil, errors.Wrap(err, "failed to create request")
	}

	if rest.Spec.Headers != nil {
		for k, v := range rest.Spec.Headers {
			req.Header.Set(k, v)
		}
	}

	if rest.Spec.Auth != nil {
		basicAuth, err := getRestAuthorization(r.Client, rest.Spec.Auth)
		if err != nil {
			return nil, errors.Wrap(err, "failed to generate basic auth")
		}
		req.Header.Set("Authorization", basicAuth)
	}

	r.Log.V(3).Info("Sending Request:", "url", newURL, "method", method, "body", newBody)

	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.Wrap(err, "http request failed")
	}
	defer resp.Body.Close()

	r.Log.V(3).Info("Response:", "statusCode", resp.StatusCode)

	bodyBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, errors.Wrap(err, "failed to read response body")
	}

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, errors.Errorf("expected response status 2xx, received status=%d body=%s", resp.StatusCode, string(bodyBytes))
	}

	return bodyBytes, nil
}

func (r *RESTManager) templateField(rest *templatev1.REST, field string) (string, error) {
	t, err := template.New("patch").Option("missingkey=zero").Funcs(r.FuncMap).Parse(field)
	// supress/ignore error if it contains text "map has no entry for key" as missingkey=zero doesn't work currently on map[string]interface{}
	// workaround for: https://github.com/golang/go/issues/24963
	if err != nil && !strings.Contains(err.Error(), "map has no entry for key") {
		return "", errors.Wrap(err, "failed to create template from field")
	}

	var tpl bytes.Buffer
	unstructuredData, err := kommons.ToUnstructured(&unstructured.Unstructured{}, rest)
	if err != nil {
		return "", errors.Wrap(err, "failed to convert rest to unstructured")
	}
	data := unstructuredData.Object

	if data["status"] == nil {
		data["status"] = map[string]interface{}{}
	}

	if err := t.Execute(&tpl, data); err != nil {
		return "", errors.Wrap(err, "failed to execute template")
	}

	return tpl.String(), nil
}

func (r *RESTManager) templateStatus(rest *templatev1.REST, response map[string]interface{}, field string) (string, error) {
	t, err := template.New("patch").Option("missingkey=zero").Funcs(r.FuncMap).Parse(field)
	// supress/ignore error if it contains text "map has no entry for key" as missingkey=zero doesn't work currently on map[string]interface{}
	// workaround for: https://github.com/golang/go/issues/24963
	if err != nil && !strings.Contains(err.Error(), "map has no entry for key") {
		return "", errors.Wrap(err, "failed to create template from field")
	}

	var tpl bytes.Buffer

	unstructuredData, err := kommons.ToUnstructured(&unstructured.Unstructured{}, rest)
	if err != nil {
		return "", errors.Wrap(err, "failed to convert rest to unstructured")
	}
	data := unstructuredData.Object
	data["response"] = response

	if err := t.Execute(&tpl, data); err != nil {
		return "", errors.Wrap(err, "failed to execute template")
	}

	return tpl.String(), nil
}

func sameGeneration(rest *templatev1.REST) bool {
	if rest.Status == nil {
		return false
	}

	observedGeneration := rest.Status["observedGeneration"]

	if observedGeneration == "" {
		return false
	}

	gen, err := strconv.ParseInt(observedGeneration, 10, 64)
	if err != nil {
		return false
	}

	return gen == rest.ObjectMeta.Generation
}

func getRestAuthorization(client *kommons.Client, auth *templatev1.RESTAuth) (string, error) {
	_, username, err := client.GetEnvValue(kommons.EnvVar{Name: "username", ValueFrom: &auth.Username}, auth.Namespace)
	if err != nil {
		return "", errors.Wrap(err, "failed to get username value")
	}
	_, password, err := client.GetEnvValue(kommons.EnvVar{Name: "password", ValueFrom: &auth.Password}, auth.Namespace)
	if err != nil {
		return "", errors.Wrap(err, "failed to get username value")
	}

	basicAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte(username+":"+password))
	return basicAuth, nil
}
