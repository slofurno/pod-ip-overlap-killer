package main

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"
)

const defaultEndpoint = "https://kubernetes.default.svc.cluster.local"

type KubeService struct {
	Metadata struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	} `json:"metadata"`
	Spec struct {
		ClusterIP string `json:"clusterIP"`
	} `json:"spec"`

	Status struct {
	} `json:"status"`
}

type KubePod struct {
	Metadata struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	} `json:"metadata"`
	Spec struct {
	} `json:"spec"`
	Status struct {
		PodIP string `json:"podIP"`
	} `json:"status"`
}

type ListServiceResponse struct {
	Items []KubeService `json:"items"`
}

type ListPodResponse struct {
	Items []KubePod `json:"items"`
}

type KubeClient struct {
	client   *http.Client
	token    string
	endpoint string
}

type Service struct {
	IP        string
	Name      string
	Namespace string
}

type Pod struct {
	IP        string
	Name      string
	Namespace string
}

func (c *KubeClient) DeletePod(namespace, name string) error {
	url := fmt.Sprintf("%s/api/v1/namespaces/%s/pods/%s", c.endpoint, namespace, name)

	req, err := http.NewRequest("DELETE", url, nil)
	if err != nil {
		return err
	}

	if err := c.do(req, nil); err != nil {
		return err
	}

	return nil
}

func (c *KubeClient) ListServices() ([]*Service, error) {
	req, err := http.NewRequest("GET", "https://kubernetes.default.svc.cluster.local/api/v1/services", nil)
	if err != nil {
		return nil, err
	}

	res := ListServiceResponse{}
	if err := c.do(req, &res); err != nil {
		return nil, err
	}

	ret := []*Service{}
	for _, svc := range res.Items {
		ret = append(ret, &Service{
			IP:        svc.Spec.ClusterIP,
			Name:      svc.Metadata.Name,
			Namespace: svc.Metadata.Namespace,
		})
	}

	return ret, nil
}

func (c *KubeClient) ListPods() ([]*Pod, error) {
	req, err := http.NewRequest("GET", "https://kubernetes.default.svc.cluster.local/api/v1/pods", nil)
	if err != nil {
		return nil, err
	}

	res := ListPodResponse{}
	if err := c.do(req, &res); err != nil {
		return nil, err
	}

	ret := []*Pod{}
	for _, pod := range res.Items {
		ret = append(ret, &Pod{
			IP:        pod.Status.PodIP,
			Name:      pod.Metadata.Name,
			Namespace: pod.Metadata.Namespace,
		})
	}

	return ret, nil
}

func (c *KubeClient) do(req *http.Request, ret interface{}) error {
	req.Header.Add("authorization", "Bearer "+string(c.token))

	res, err := c.client.Do(req)
	if err != nil {
		return err
	}

	defer res.Body.Close()

	b, err := ioutil.ReadAll(res.Body)
	if err != nil {
		return err
	}

	switch res.StatusCode {
	case 200, 201, 202:
	default:
		return fmt.Errorf("bad status code: %d (%s)", res.StatusCode, string(b))
	}

	if ret != nil {
		if err := json.Unmarshal(b, &ret); err != nil {
			return err
		}
	}

	return nil
}

type PodCidrOverlapKiller struct {
	kubeClient           *KubeClient
	deleteOverlappedPods bool
	intervalSeconds      int
}

func (s *PodCidrOverlapKiller) Watch() {
	for {
		time.Sleep(time.Duration(s.intervalSeconds) * time.Second)
		serviceIPs := map[string]*Service{}

		services, err := s.kubeClient.ListServices()
		if err != nil {
			log.Println(err)
			continue
		}

		for _, svc := range services {
			serviceIPs[svc.IP] = svc
		}

		pods, err := s.kubeClient.ListPods()
		if err != nil {
			log.Println(err)
			continue
		}

		for _, pod := range pods {
			if svc, ok := serviceIPs[pod.IP]; ok {
				log.Printf(
					"%s/service/%s and %s/pod/%s overlap (%s)\n",
					svc.Namespace,
					svc.Name,
					pod.Namespace,
					pod.Name,
					pod.IP,
				)

				if s.deleteOverlappedPods {
					if err := s.kubeClient.DeletePod(pod.Namespace, pod.Name); err != nil {
						log.Printf("error deleting %s/pod/%s: %v\n", pod.Namespace, pod.Name, err)
					}
				}
			}
		}
	}
}

func main() {
	deleteOverlappedPods := false
	intervalSeconds := 10

	if v := os.Getenv("DELETE_PODS"); v != "" {
		deleteOverlappedPods = true
	}

	if v := os.Getenv("INTERVAL_SECONDS"); v != "" {
		i, err := strconv.Atoi(v)
		if err != nil {
			log.Printf("bad INTERVAL_SECONDS %s", v)
		} else {
			intervalSeconds = i
		}
	}

	cert, err := ioutil.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/ca.crt")
	if err != nil {
		panic(err)
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(cert) {
		panic(err)
	}

	client := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   30 * time.Second,
				KeepAlive: 30 * time.Second,
				DualStack: true,
			}).DialContext,
			MaxIdleConns:          100,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
			TLSClientConfig: &tls.Config{
				MinVersion: tls.VersionTLS12,
				RootCAs:    pool,
			},
		},
	}

	token, err := ioutil.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/token")
	if err != nil {
		panic(err)
	}

	kc := &KubeClient{
		client:   client,
		token:    string(token),
		endpoint: defaultEndpoint,
	}

	killer := &PodCidrOverlapKiller{
		kubeClient:           kc,
		deleteOverlappedPods: deleteOverlappedPods,
		intervalSeconds:      intervalSeconds,
	}
	killer.Watch()
}
