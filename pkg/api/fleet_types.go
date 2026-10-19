package api

import "encoding/json"

// Go mirror of the fleet API contract (backstage packages/common/src/models/fleet.ts).
// JSON names are the contract's; only what tnctl reads or writes is modelled. Commands print the
// server's JSON untouched with -o json, so a field missing here is never lost to the user.

// Override layers of a configuration resource, lowest priority first.
const (
	LayerPlatform = "platform"
	LayerType     = "type"
	LayerFleet    = "fleet"
	LayerRobot    = "robot"
	LayerLocal    = "local"
	LayerOverride = "override"
)

// Override states (Device status config.overrides[].state; "pending" is client side only).
const (
	StateActive        = "active"
	StateRedundant     = "redundant"
	StateNoTarget      = "no-target"
	StateInvalid       = "invalid"
	StateNotApplicable = "not-applicable"
	StatePending       = "pending"
)

// Override storage: where an override of a resource lives.
const (
	StorageDeviceOverrides = "device-overrides"
	StorageLocalItem       = "local-item"
)

// GitOpsApplicationRef is a git application of a layer.
type GitOpsApplicationRef struct {
	RepoURL  string `json:"repo_url"`
	Revision string `json:"revision,omitempty"`
	Path     string `json:"path"`
	NodeRole string `json:"node_role,omitempty"`
}

// GitRefInfo says what a revision is.
type GitRefInfo struct {
	Kind     string `json:"kind"`
	Name     string `json:"name"`
	Commit   string `json:"commit,omitempty"`
	NewerTag string `json:"newerTag,omitempty"`
	Warning  string `json:"warning,omitempty"`
}

// GitOpsApplicationView is an application reference with its revision explained.
type GitOpsApplicationView struct {
	GitOpsApplicationRef
	Ref *GitRefInfo `json:"ref,omitempty"`
}

// FleetNode is one computer of a robot.
type FleetNode struct {
	Role     string `json:"role"`
	NodeName string `json:"nodeName"`
	Joined   bool   `json:"joined"`
	Ready    bool   `json:"ready"`
	K3s      string `json:"k3s,omitempty"`
	Hardware string `json:"hardware,omitempty"`
	Optional bool   `json:"optional,omitempty"`
}

// RobotLayerSummary counts what a configuration layer contributes to a robot.
type RobotLayerSummary struct {
	Layer        string `json:"layer"`
	Source       string `json:"source"`
	Applications int    `json:"applications"`
}

// RobotSummary is one row of GET robots.
type RobotSummary struct {
	Name             string              `json:"name"`
	Namespace        string              `json:"namespace"`
	Type             string              `json:"type"`
	TypeDisplayName  string              `json:"typeDisplayName,omitempty"`
	Labels           map[string]string   `json:"labels"`
	Device           string              `json:"device"`
	Phase            string              `json:"phase"`
	PhaseMessage     string              `json:"phaseMessage,omitempty"`
	Nodes            []FleetNode         `json:"nodes"`
	Layers           []RobotLayerSummary `json:"layers"`
	Deviations       int                 `json:"deviations"`
	Overrides        int                 `json:"overrides,omitempty"`
	OldestOverrideAt string              `json:"oldestOverrideAt,omitempty"`
	Fleets           []string            `json:"fleets"`
	CreatedAt        string              `json:"createdAt,omitempty"`
}

// RobotDetail is GET robots/:name.
type RobotDetail struct {
	RobotSummary
	Spec struct {
		Type          string `json:"type"`
		Device        string `json:"device,omitempty"`
		ServerAddress string `json:"serverAddress,omitempty"`
	} `json:"spec"`
	Configuration RobotConfigurationView `json:"configuration"`
}

// ValueProvenance says where one leaf value of a merged HelmChart comes from.
type ValueProvenance struct {
	Path     string `json:"path"`
	Value    any    `json:"value"`
	Layer    string `json:"layer"`
	Source   string `json:"source"`
	Injected string `json:"injected,omitempty"`
}

// ConfigLinks are GitHub links for the manifest file a resource came from.
type ConfigLinks struct {
	Repo          string `json:"repo"`
	View          string `json:"view"`
	Permalink     string `json:"permalink,omitempty"`
	History       string `json:"history,omitempty"`
	Commit        string `json:"commit,omitempty"`
	Edit          string `json:"edit,omitempty"`
	EditOnDefault string `json:"editOnDefault,omitempty"`
	Pinned        bool   `json:"pinned"`
}

// ChartLinks are links to a Helm chart's source.
type ChartLinks struct {
	Owner      string `json:"owner"`
	Repo       string `json:"repo"`
	Tag        string `json:"tag,omitempty"`
	ChartDir   string `json:"chartDir"`
	Source     string `json:"source,omitempty"`
	Values     string `json:"values,omitempty"`
	ValuesEdit string `json:"valuesEdit,omitempty"`
	Release    string `json:"release,omitempty"`
	Guessed    bool   `json:"guessed"`
}

// ChartInfo is the chart of a HelmChart resource.
type ChartInfo struct {
	Chart           string `json:"chart,omitempty"`
	Version         string `json:"version,omitempty"`
	Repo            string `json:"repo,omitempty"`
	TargetNamespace string `json:"targetNamespace,omitempty"`
}

// ContainerImage is one container image of a pod-template resource.
type ContainerImage struct {
	Container string `json:"container"`
	Image     string `json:"image"`
	Init      bool   `json:"init,omitempty"`
}

// ResourceOverrideInfo summarises the override on an effective resource.
type ResourceOverrideInfo struct {
	Storage string            `json:"storage"`
	State   string            `json:"state,omitempty"`
	Message string            `json:"message,omitempty"`
	Note    string            `json:"note,omitempty"`
	SetBy   string            `json:"setBy,omitempty"`
	SetAt   string            `json:"setAt,omitempty"`
	Paths   []string          `json:"paths"`
	Version string            `json:"version,omitempty"`
	Images  map[string]string `json:"images,omitempty"`
}

// FleetValues are the merged values of a HelmChart with provenance per leaf.
type FleetValues struct {
	Merged     map[string]any    `json:"merged"`
	Provenance []ValueProvenance `json:"provenance"`
}

// FleetConfigResource is one resource of a robot's configuration.
type FleetConfigResource struct {
	Identity    string                 `json:"identity"`
	Kind        string                 `json:"kind"`
	Name        string                 `json:"name"`
	Namespace   string                 `json:"namespace"`
	Layer       string                 `json:"layer"`
	Source      string                 `json:"source"`
	Application *GitOpsApplicationView `json:"application,omitempty"`
	Status      string                 `json:"status"`
	Overlay     bool                   `json:"overlay,omitempty"`
	Chart       *ChartInfo             `json:"chart,omitempty"`
	NodeRole    string                 `json:"nodeRole,omitempty"`
	Values      *FleetValues           `json:"values,omitempty"`
	MergedFrom  []string               `json:"mergedFrom,omitempty"`
	File        string                 `json:"file,omitempty"`
	Links       *ConfigLinks           `json:"links,omitempty"`
	ChartLinks  *ChartLinks            `json:"chartLinks,omitempty"`
	Images      []ContainerImage       `json:"images,omitempty"`
	Override    *ResourceOverrideInfo  `json:"override,omitempty"`
}

// ID is the resource id used by the override routes: Kind/namespace/name.
func (r FleetConfigResource) ID() string {
	ns := r.Namespace
	if ns == "" {
		ns = "default"
	}
	return r.Kind + "/" + ns + "/" + r.Name
}

// RobotConfigurationView is the layered configuration of a robot or device.
type RobotConfigurationView struct {
	Resources  []FleetConfigResource `json:"resources"`
	Effective  []FleetConfigResource `json:"effective"`
	Deviations []struct {
		Identity string `json:"identity"`
		Reason   string `json:"reason"`
	} `json:"deviations"`
	Sync []struct {
		Application GitOpsApplicationView `json:"application"`
		Status      string                `json:"status"`
	} `json:"sync,omitempty"`
	Warnings []string `json:"warnings"`
}

// ResourceOverride is Device.spec.overrides.resources[id].
type ResourceOverride struct {
	ValuesContent string            `json:"valuesContent,omitempty"`
	Version       string            `json:"version,omitempty"`
	Images        map[string]string `json:"images,omitempty"`
	Note          string            `json:"note,omitempty"`
	SetBy         string            `json:"setBy,omitempty"`
	SetAt         string            `json:"setAt,omitempty"`
}

// ApplicationOverride is a "try another revision" entry.
type ApplicationOverride struct {
	RepoURL  string `json:"repo_url"`
	Path     string `json:"path,omitempty"`
	Revision string `json:"revision"`
	Note     string `json:"note,omitempty"`
	SetBy    string `json:"setBy,omitempty"`
	SetAt    string `json:"setAt,omitempty"`
}

// DeviceOverrides is Device.spec.overrides.
type DeviceOverrides struct {
	Revision     string                      `json:"revision,omitempty"`
	Resources    map[string]ResourceOverride `json:"resources,omitempty"`
	Applications []ApplicationOverride       `json:"applications,omitempty"`
}

// OverrideStatus is the controller's verdict on one override.
type OverrideStatus struct {
	Target  string `json:"target"`
	State   string `json:"state"`
	Message string `json:"message,omitempty"`
}

// ConfigTargetRef says what a target name resolved to.
type ConfigTargetRef struct {
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Device string `json:"device"`
	Robot  string `json:"robot,omitempty"`
}

// PublishedInfo is what the controller last published to the device.
type PublishedInfo struct {
	Hash       string `json:"hash,omitempty"`
	LastChange string `json:"lastChange,omitempty"`
	Message    string `json:"message,omitempty"`
}

// ConfigTargetView is GET config/:target.
type ConfigTargetView struct {
	Target            ConfigTargetRef        `json:"target"`
	Configuration     RobotConfigurationView `json:"configuration"`
	Overrides         DeviceOverrides        `json:"overrides"`
	OverrideStatus    []OverrideStatus       `json:"overrideStatus"`
	OverridesRevision string                 `json:"overridesRevision,omitempty"`
	Published         *PublishedInfo         `json:"published,omitempty"`
	OverrideCount     int                    `json:"overrideCount"`
	OldestOverrideAt  string                 `json:"oldestOverrideAt,omitempty"`
}

// ImageKey is a place in a resource where an image is set.
type ImageKey struct {
	Path    string `json:"path"`
	Form    string `json:"form"`
	Current string `json:"current,omitempty"`
	Source  string `json:"source"`
	Primary bool   `json:"primary"`
	Init    bool   `json:"init,omitempty"`
}

// ConfigResourceDetail is GET config/:target/resource.
type ConfigResourceDetail struct {
	Target        ConfigTargetRef       `json:"target"`
	ID            string                `json:"id"`
	Resource      FleetConfigResource   `json:"resource"`
	Contributions []FleetConfigResource `json:"contributions"`
	Rendered      map[string]any        `json:"rendered,omitempty"`
	Base          map[string]any        `json:"base,omitempty"`
	Override      *struct {
		ResourceOverride
		Storage string `json:"storage"`
	} `json:"override,omitempty"`
	OverrideStatus *OverrideStatus `json:"overrideStatus,omitempty"`
	Images         []ImageKey      `json:"images"`
	ChartDefaults  map[string]any  `json:"chartDefaults,omitempty"`
	Editable       struct {
		Values  bool   `json:"values"`
		Version bool   `json:"version"`
		Images  bool   `json:"images"`
		Reason  string `json:"reason,omitempty"`
	} `json:"editable"`
}

// ValueOp is one value edit. Op is "set", "delete" or "unset"; Value only for set.
type ValueOp struct {
	Op    string
	Path  string
	Value any
}

// MarshalJSON always sends the value of a "set" (also false, 0, "" and null) and never
// the value of the other ops.
func (o ValueOp) MarshalJSON() ([]byte, error) {
	m := map[string]any{"op": o.Op, "path": o.Path}
	if o.Op == "set" {
		m["value"] = o.Value
	}
	return json.Marshal(m)
}

// ImageChange is the `image` field of an OverrideResourcePatch.
type ImageChange struct {
	Path  string `json:"path,omitempty"`
	Value string `json:"value"`
}

// OverrideResourcePatch is the body of PATCH overrides/resource.
type OverrideResourcePatch struct {
	Ops []ValueOp
	// Version sets the chart version; ClearVersion removes the version override.
	Version      string
	ClearVersion bool
	// Images maps a container to its image; a nil value removes that container's override.
	Images map[string]*string
	// Image changes an image the platform found (HelmChart image keys or a container).
	Image *ImageChange
	// Note sets the note; ClearNote removes it.
	Note      string
	ClearNote bool
}

// MarshalJSON writes only what is set; "version" and "note" are null when cleared.
func (p OverrideResourcePatch) MarshalJSON() ([]byte, error) {
	m := map[string]any{}
	if len(p.Ops) > 0 {
		m["ops"] = p.Ops
	}
	switch {
	case p.ClearVersion:
		m["version"] = nil
	case p.Version != "":
		m["version"] = p.Version
	}
	if len(p.Images) > 0 {
		m["images"] = p.Images
	}
	if p.Image != nil {
		m["image"] = p.Image
	}
	switch {
	case p.ClearNote:
		m["note"] = nil
	case p.Note != "":
		m["note"] = p.Note
	}
	return json.Marshal(m)
}

// Empty reports whether the patch changes nothing.
func (p OverrideResourcePatch) Empty() bool {
	return len(p.Ops) == 0 && p.Version == "" && !p.ClearVersion && len(p.Images) == 0 &&
		p.Image == nil && p.Note == "" && !p.ClearNote
}

// OverrideWriteResult is the answer of every override write.
type OverrideWriteResult struct {
	Target    ConfigTargetRef      `json:"target"`
	ID        string               `json:"id,omitempty"`
	Storage   string               `json:"storage,omitempty"`
	Revision  string               `json:"revision,omitempty"`
	DryRun    bool                 `json:"dryRun"`
	Before    *FleetConfigResource `json:"before,omitempty"`
	After     *FleetConfigResource `json:"after,omitempty"`
	Overrides DeviceOverrides      `json:"overrides"`
	Warnings  []string             `json:"warnings"`
}

// PromoteResult is GET config/:target/promote.
type PromoteResult struct {
	YAML       string `json:"yaml"`
	RepoURL    string `json:"repo_url,omitempty"`
	Branch     string `json:"branch,omitempty"`
	Path       string `json:"path,omitempty"`
	NewFileURL string `json:"newFileUrl,omitempty"`
	EditURL    string `json:"editUrl,omitempty"`
	Note       string `json:"note"`
}
