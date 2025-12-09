/*
Copyright 2025 Kamal.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

type HFTokenSecretRef struct {
	Name string `json:"name"`
	Key  string `json:"key"`
}

type DownloadSettings struct {
	TimeoutMillis    int32  `json:"timeoutMillis,omitempty"`
	EnableHFTransfer bool   `json:"enableHFTransfer,omitempty"`
	KeepAliveSeconds int32  `json:"keepAliveSeconds,omitempty"`
	CPU              string `json:"cpu,omitempty"`
	Memory           string `json:"memory,omitempty"`
	ForceReload      bool   `json:"forceReload,omitempty"`
}

type ModelSpec struct {
	Name       string `json:"name"`                 // huggingface model name
	SHA        string `json:"sha,omitempty"`        // optional checksum
	RetryCount int32  `json:"retryCount,omitempty"` // optional retry config
}
type ModelDownloadSpec struct {
	Models     []ModelSpec       `json:"models"`
	StoragePVC string            `json:"storagePVC"`
	HFTokenRef *HFTokenSecretRef `json:"hfTokenRef"`
	NodePool   string            `json:"nodePool,omitempty"`
	Settings   DownloadSettings  `json:"settings,omitempty"`
}

type ModelDownloadStatus struct {
	Pending   []string `json:"pending,omitempty"`
	Completed []string `json:"completed,omitempty"`
	Failed    []string `json:"failed,omitempty"`
}

// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Pending",type=string,JSONPath=`.status.pending`
// +kubebuilder:printcolumn:name="Completed",type=string,JSONPath=`.status.completed`
// +kubebuilder:printcolumn:name="Failed",type=string,JSONPath=`.status.failed`

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// ModelDownload is the Schema for the modeldownloads API
type ModelDownload struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of ModelDownload
	// +required
	Spec ModelDownloadSpec `json:"spec"`

	// status defines the observed state of ModelDownload
	// +optional
	Status ModelDownloadStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// ModelDownloadList contains a list of ModelDownload
type ModelDownloadList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []ModelDownload `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ModelDownload{}, &ModelDownloadList{})
}
