package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Go mirror of the read-only inspect routes of the fleet API (InspectPodResponse,
// InspectLogsResponse and InspectReleaseResponse in the platform's contract). They are answered by the
// robot's web-terminal pod: the logs, the pod and the installed objects of a failing app. A user needs
// write access and permission to open a terminal; a TeknoirBot reaches them with its own key and reads
// them without any.

// InspectContainerState is the state of one container.
type InspectContainerState struct {
	// Kind is running, waiting, terminated or unknown.
	Kind       string `json:"kind"`
	Reason     string `json:"reason,omitempty"`
	Message    string `json:"message,omitempty"`
	ExitCode   *int   `json:"exitCode,omitempty"`
	Signal     *int   `json:"signal,omitempty"`
	StartedAt  string `json:"startedAt,omitempty"`
	FinishedAt string `json:"finishedAt,omitempty"`
}

// InspectEnvValue is one environment variable of a container; the values of credentials are hidden.
type InspectEnvValue struct {
	Name  string `json:"name"`
	Value string `json:"value,omitempty"`
	// From is where the value comes from instead, for example "secret db/password".
	From string `json:"from,omitempty"`
}

// InspectPort is one container port.
type InspectPort struct {
	ContainerPort int    `json:"containerPort"`
	Protocol      string `json:"protocol,omitempty"`
	Name          string `json:"name,omitempty"`
}

// InspectMount is one volume mount of a container.
type InspectMount struct {
	Name      string `json:"name"`
	MountPath string `json:"mountPath"`
	ReadOnly  bool   `json:"readOnly,omitempty"`
}

// InspectResources are the requests and limits of a container.
type InspectResources struct {
	Requests map[string]string `json:"requests,omitempty"`
	Limits   map[string]string `json:"limits,omitempty"`
}

// InspectContainer is one container of a pod.
type InspectContainer struct {
	Name     string                `json:"name"`
	Init     bool                  `json:"init"`
	Image    string                `json:"image"`
	Ready    bool                  `json:"ready"`
	Restarts int                   `json:"restarts"`
	State    InspectContainerState `json:"state"`
	// LastTerminated is the state the container left when it last ran.
	LastTerminated *InspectContainerState `json:"lastTerminated,omitempty"`
	OOMKilled      bool                   `json:"oomKilled"`
	Resources      *InspectResources      `json:"resources,omitempty"`
	Ports          []InspectPort          `json:"ports,omitempty"`
	Env            []InspectEnvValue      `json:"env,omitempty"`
	EnvFrom        []string               `json:"envFrom,omitempty"`
	Command        []string               `json:"command,omitempty"`
	Args           []string               `json:"args,omitempty"`
	Mounts         []InspectMount         `json:"mounts,omitempty"`
}

// InspectCondition is one condition of a pod.
type InspectCondition struct {
	Type               string `json:"type"`
	Status             string `json:"status"`
	Reason             string `json:"reason,omitempty"`
	Message            string `json:"message,omitempty"`
	LastTransitionTime string `json:"lastTransitionTime,omitempty"`
}

// InspectOwner is the object that owns a pod.
type InspectOwner struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

// InspectPod is the pod as the robot reads it.
type InspectPod struct {
	Name      string        `json:"name"`
	Namespace string        `json:"namespace"`
	Node      string        `json:"node,omitempty"`
	Phase     string        `json:"phase"`
	Reason    string        `json:"reason,omitempty"`
	Message   string        `json:"message,omitempty"`
	QOSClass  string        `json:"qosClass,omitempty"`
	StartTime string        `json:"startTime,omitempty"`
	PodIP     string        `json:"podIP,omitempty"`
	Owner     *InspectOwner `json:"owner,omitempty"`
	// Restarts is the sum of the restarts of all containers.
	Restarts       int                `json:"restarts"`
	Conditions     []InspectCondition `json:"conditions"`
	Containers     []InspectContainer `json:"containers"`
	InitContainers []InspectContainer `json:"initContainers"`
	Labels         map[string]string  `json:"labels"`
}

// InspectEvent is one event of the pod.
type InspectEvent struct {
	// Type is Normal or Warning.
	Type    string `json:"type"`
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
	Count   int    `json:"count,omitempty"`
	First   string `json:"first,omitempty"`
	Last    string `json:"last,omitempty"`
	Source  string `json:"source,omitempty"`
}

// InspectPodResponse is GET robots/:name/inspect/pods/:podNamespace/:pod.
type InspectPodResponse struct {
	Robot           string         `json:"robot"`
	Device          string         `json:"device"`
	Pod             InspectPod     `json:"pod"`
	Events          []InspectEvent `json:"events"`
	EventsTruncated bool           `json:"eventsTruncated"`
	FetchedAt       string         `json:"fetchedAt"`
}

// InspectTruncated says which limit cut the log off.
type InspectTruncated struct {
	Bytes bool `json:"bytes"`
	Lines bool `json:"lines"`
}

// InspectLogsResponse is GET robots/:name/inspect/pods/:podNamespace/:pod/logs.
type InspectLogsResponse struct {
	Robot     string `json:"robot"`
	Device    string `json:"device"`
	Namespace string `json:"namespace"`
	Pod       string `json:"pod"`
	// Container is nil when the pod has one container.
	Container *string          `json:"container"`
	Previous  bool             `json:"previous"`
	Tail      int              `json:"tail"`
	Since     string           `json:"since,omitempty"`
	Text      string           `json:"text"`
	Lines     int              `json:"lines"`
	Bytes     int              `json:"bytes"`
	Truncated InspectTruncated `json:"truncated"`
	// Redacted is the number of lines the credential filter changed.
	Redacted  int    `json:"redacted"`
	FetchedAt string `json:"fetchedAt"`
}

// InspectReleaseObject is one object of an installed Helm release's manifest.
type InspectReleaseObject struct {
	// Index is the position of the document in the manifest (0-based).
	Index      int    `json:"index"`
	APIVersion string `json:"apiVersion,omitempty"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	// Namespace is empty for a cluster-scoped object.
	Namespace string `json:"namespace"`
	Source    string `json:"source,omitempty"`
	// Hidden: a value in the document is hidden (always true for a Secret).
	Hidden bool `json:"hidden"`
}

// InspectReleaseHistory is one revision of an installed release.
type InspectReleaseHistory struct {
	Revision int    `json:"revision"`
	Status   string `json:"status"`
}

// InspectReleaseResponse is GET robots/:name/inspect/releases/:releaseNamespace/:release.
type InspectReleaseResponse struct {
	Robot        string                  `json:"robot"`
	Device       string                  `json:"device"`
	Name         string                  `json:"name"`
	Namespace    string                  `json:"namespace"`
	Revision     int                     `json:"revision"`
	Status       string                  `json:"status"`
	Chart        string                  `json:"chart"`
	ChartVersion string                  `json:"chartVersion"`
	AppVersion   string                  `json:"appVersion,omitempty"`
	LastDeployed string                  `json:"lastDeployed,omitempty"`
	History      []InspectReleaseHistory `json:"history"`
	Objects      []InspectReleaseObject  `json:"objects"`
	// YAML is the installed manifest, with Secret values hidden.
	YAML      string `json:"yaml"`
	FetchedAt string `json:"fetchedAt"`
}

// inspectQuery is the query every inspect route gets: enable=false asks the platform not to turn the
// robot's Shell on. When it is off the platform answers 409 with the code terminal_off.
func inspectQuery() url.Values { return url.Values{"enable": {"false"}} }

// inspectVersionErr names an older platform for what it is: one whose inspect routes do not know
// enable=false, so it can only read a robot by turning the Shell on, which tnctl never asks it to do.
// Any other error passes through unchanged.
func inspectVersionErr(err error) error {
	var ae *Error
	if errors.As(err, &ae) && ae.Status == http.StatusBadRequest &&
		strings.Contains(ae.Message, "query parameter enable is not known") {
		ae.Message = "this platform is older than this tnctl: it cannot read a robot without turning its Shell on, so tnctl does not ask it (ask for a platform update)"
	}
	return err
}

// InspectPod reads a pod of a robot and its recent events.
func (f *Fleet) InspectPod(ctx context.Context, robot, podNamespace, pod string) (*InspectPodResponse, json.RawMessage, error) {
	r, raw, err := decode[InspectPodResponse](f.Do(ctx, http.MethodGet,
		"robots/"+url.PathEscape(robot)+"/inspect/pods/"+url.PathEscape(podNamespace)+"/"+url.PathEscape(pod),
		inspectQuery(), nil))
	return &r, raw, inspectVersionErr(err)
}

// InspectLogsOptions are the flags of one logs call. The zero value asks for the server's defaults.
type InspectLogsOptions struct {
	Container string
	Previous  bool
	// Tail is the number of lines from the end; 0 leaves the server's default.
	Tail int
	// Since is a duration such as 10m; empty means no limit.
	Since string
}

// InspectLogs reads the log of one pod (of one container, when it has several).
func (f *Fleet) InspectLogs(ctx context.Context, robot, podNamespace, pod string, o InspectLogsOptions) (*InspectLogsResponse, json.RawMessage, error) {
	q := inspectQuery()
	if o.Container != "" {
		q.Set("container", o.Container)
	}
	if o.Previous {
		q.Set("previous", "true")
	}
	if o.Tail != 0 {
		q.Set("tail", strconv.Itoa(o.Tail))
	}
	if o.Since != "" {
		q.Set("since", o.Since)
	}
	r, raw, err := decode[InspectLogsResponse](f.Do(ctx, http.MethodGet,
		"robots/"+url.PathEscape(robot)+"/inspect/pods/"+url.PathEscape(podNamespace)+"/"+url.PathEscape(pod)+"/logs",
		q, nil))
	return &r, raw, inspectVersionErr(err)
}

// InspectRelease reads an installed Helm release of a robot: its revision, status and objects.
func (f *Fleet) InspectRelease(ctx context.Context, robot, releaseNamespace, release string) (*InspectReleaseResponse, json.RawMessage, error) {
	r, raw, err := decode[InspectReleaseResponse](f.Do(ctx, http.MethodGet,
		"robots/"+url.PathEscape(robot)+"/inspect/releases/"+url.PathEscape(releaseNamespace)+"/"+url.PathEscape(release),
		inspectQuery(), nil))
	return &r, raw, inspectVersionErr(err)
}
