package controller

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/containous/traefik/v2/pkg/safe"
	"github.com/containous/traefik/v2/pkg/testhelpers"
	smiAccessv1alpha1 "github.com/deislabs/smi-sdk-go/pkg/apis/access/v1alpha1"
	smiSpecsv1alpha1 "github.com/deislabs/smi-sdk-go/pkg/apis/specs/v1alpha1"
	smiSplitv1alpha1 "github.com/deislabs/smi-sdk-go/pkg/apis/split/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
)

func TestEnableReadiness(t *testing.T) {
	config := safe.Safe{}
	api := NewAPI(9000, &config, nil, nil, "foo")

	assert.Equal(t, false, api.readiness)

	api.EnableReadiness()

	assert.Equal(t, true, api.readiness)
}

func TestGetReadiness(t *testing.T) {
	testCases := []struct {
		desc               string
		readiness          bool
		expectedStatusCode int
	}{
		{
			desc:               "ready",
			readiness:          true,
			expectedStatusCode: http.StatusOK,
		},
		{
			desc:               "not ready",
			readiness:          false,
			expectedStatusCode: http.StatusInternalServerError,
		},
	}

	for _, test := range testCases {
		test := test
		t.Run(test.desc, func(t *testing.T) {
			t.Parallel()
			config := safe.Safe{}
			api := NewAPI(9000, &config, nil, nil, "foo")
			api.readiness = test.readiness

			res := httptest.NewRecorder()
			req := testhelpers.MustNewRequest(http.MethodGet, "/api/status/readiness", nil)

			api.getReadiness(res, req)

			assert.Equal(t, test.expectedStatusCode, res.Code)
		})
	}
}

func TestGetCurrentConfiguration(t *testing.T) {
	config := safe.Safe{}
	api := NewAPI(9000, &config, nil, nil, "foo")

	config.Set("foo")

	res := httptest.NewRecorder()
	req := testhelpers.MustNewRequest(http.MethodGet, "/api/configuration/current", nil)

	api.getCurrentConfiguration(res, req)

	assert.Equal(t, "\"foo\"\n", res.Body.String())
}

func TestGetDeployLog(t *testing.T) {
	config := safe.Safe{}
	log := NewDeployLog(1000)
	api := NewAPI(9000, &config, log, nil, "foo")

	currentTime := time.Now()
	log.LogDeploy(currentTime, "foo", "bar", true, "blabla")

	data, err := currentTime.MarshalJSON()
	assert.NoError(t, err)

	currentTimeString := string(data)
	expected := fmt.Sprintf("[{\"TimeStamp\":%s,\"PodName\":\"foo\",\"PodIP\":\"bar\",\"DeploySuccessful\":true,\"Reason\":\"blabla\"}]", currentTimeString)

	res := httptest.NewRecorder()
	req := testhelpers.MustNewRequest(http.MethodGet, "/api/configuration/current", nil)

	api.getDeployLog(res, req)
	assert.Equal(t, expected, res.Body.String())
	assert.Equal(t, http.StatusOK, res.Code)
}

func TestGetMeshNodes(t *testing.T) {
	testCases := []struct {
		desc string

		pods        *v1.PodList
		listPodsErr error

		expectedPodInfoList   []podInfo
		expectedErrorResponse string
		expectedStatusCode    int
	}{
		{
			desc: "mix of ready/unready pods",
			pods: &v1.PodList{
				Items: []v1.Pod{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name: "titi",
						},
						Status: v1.PodStatus{
							PodIP: "1.2.3.4",
						},
					},
					{
						ObjectMeta: metav1.ObjectMeta{
							Name: "toto",
						},
						Status: v1.PodStatus{
							PodIP:             "5.6.7.8",
							ContainerStatuses: []v1.ContainerStatus{{Ready: false}},
						},
					},
				},
			},
			expectedPodInfoList: []podInfo{
				{Name: "titi", IP: "1.2.3.4", Ready: true},
				{Name: "toto", IP: "5.6.7.8", Ready: false},
			},
			expectedStatusCode: http.StatusOK,
		},
		{
			desc:                  "pod list error",
			listPodsErr:           errors.New("no pods found"),
			expectedErrorResponse: "unable to retrieve pod list: no pods found",
			expectedStatusCode:    http.StatusInternalServerError,
		},
	}

	for _, test := range testCases {
		test := test
		t.Run(test.desc, func(t *testing.T) {
			t.Parallel()
			var calledWithNamespace string
			clientMock := &podListerMock{
				listPodWithOptions: func(namespace string, opts metav1.ListOptions) (*v1.PodList, error) {
					calledWithNamespace = namespace
					return test.pods, test.listPodsErr
				},
			}

			config := safe.Safe{}
			api := NewAPI(9000, &config, nil, clientMock, "foo")

			res := httptest.NewRecorder()
			req := testhelpers.MustNewRequest(http.MethodGet, "/api/status/readiness", nil)

			api.getMeshNodes(res, req)

			assert.Equal(t, test.expectedStatusCode, res.Code)
			body, err := ioutil.ReadAll(res.Body)
			require.NoError(t, err)

			assert.Equal(t, "foo", calledWithNamespace)

			if test.expectedStatusCode != http.StatusOK {
				assert.Contains(t, string(body), test.expectedErrorResponse)
			} else {
				var podInfoList []podInfo
				err := json.Unmarshal(body, &podInfoList)
				require.NoError(t, err)

				assert.Equal(t, podInfoList, test.expectedPodInfoList)
			}
		})
	}
}

type podListerMock struct {
	listPodWithOptions func(string, metav1.ListOptions) (*v1.PodList, error)
}

func (pl *podListerMock) ListPodWithOptions(namespace string, opts metav1.ListOptions) (*v1.PodList, error) {
	return pl.listPodWithOptions(namespace, opts)
}

// Unimplemented methods below.

func (pl *podListerMock) GetNamespace(name string) (*v1.Namespace, bool, error) {
	panic("implement me")
}

func (pl *podListerMock) GetNamespaces() ([]*v1.Namespace, error) {
	panic("implement me")
}

func (pl *podListerMock) GetService(namespace, name string) (*v1.Service, bool, error) {
	panic("implement me")
}

func (pl *podListerMock) GetServices(namespace string) ([]*v1.Service, error) {
	panic("implement me")
}

func (pl *podListerMock) ListServicesWithOptions(namespace string, options metav1.ListOptions) (*v1.ServiceList, error) {
	panic("implement me")
}

func (pl *podListerMock) WatchServicesWithOptions(namespace string, options metav1.ListOptions) (watch.Interface, error) {
	panic("implement me")
}

func (pl *podListerMock) CreateService(service *v1.Service) (*v1.Service, error) {
	panic("implement me")
}

func (pl *podListerMock) UpdateService(service *v1.Service) (*v1.Service, error) {
	panic("implement me")
}

func (pl *podListerMock) DeleteService(namespace, name string) error {
	panic("implement me")
}

func (pl *podListerMock) GetEndpoints(namespace, name string) (*v1.Endpoints, bool, error) {
	panic("implement me")
}

func (pl *podListerMock) GetEndpointses(namespace string) ([]*v1.Endpoints, error) {
	panic("implement me")
}

func (pl *podListerMock) GetPod(namespace, name string) (*v1.Pod, bool, error) {
	panic("implement me")
}

func (pl *podListerMock) GetConfigMap(namespace, name string) (*v1.ConfigMap, bool, error) {
	panic("implement me")
}

func (pl *podListerMock) UpdateConfigMap(configMap *v1.ConfigMap) (*v1.ConfigMap, error) {
	panic("implement me")
}

func (pl *podListerMock) CreateConfigMap(configMap *v1.ConfigMap) (*v1.ConfigMap, error) {
	panic("implement me")
}

func (pl *podListerMock) GetDeployment(namespace, name string) (*appsv1.Deployment, bool, error) {
	panic("implement me")
}

func (pl *podListerMock) UpdateDeployment(deployment *appsv1.Deployment) (*appsv1.Deployment, error) {
	panic("implement me")
}

func (pl *podListerMock) GetTrafficTargets() ([]*smiAccessv1alpha1.TrafficTarget, error) {
	panic("implement me")
}

func (pl *podListerMock) GetHTTPRouteGroup(namespace, name string) (*smiSpecsv1alpha1.HTTPRouteGroup, bool, error) {
	panic("implement me")
}

func (pl *podListerMock) GetTCPRoute(namespace, name string) (*smiSpecsv1alpha1.TCPRoute, bool, error) {
	panic("implement me")
}

func (pl *podListerMock) GetTrafficSplits() ([]*smiSplitv1alpha1.TrafficSplit, error) {
	panic("implement me")
}
