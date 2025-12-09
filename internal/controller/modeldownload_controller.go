/*
Copyright 2025 Kamal.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	hfopsv1alpha1 "github.com/skamalj/hf-operator/api/v1alpha1"
)
import _ "embed"

// RBAC
// (these markers will be used by controller-gen / make manifests to create RBAC rules)
//
// Allow full access to ModelDownload CR (generated earlier by kubebuilder)
// +kubebuilder:rbac:groups=hfops.kamal.dev,resources=modeldownloads,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=hfops.kamal.dev,resources=modeldownloads/status,verbs=get;update;patch
// Allow reading secrets referenced by CR
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// Allow creating/managing Jobs and Pods
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;create;update;patch;delete
// Allow events (useful for recording)
/// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch;update;list;watch

// ModelDownloadReconciler reconciles a ModelDownload object
type ModelDownloadReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//go:embed scripts/download.sh
var downloadScript string

// sanitize converts model or CR names to safe pod/job names.
func sanitize(name string) string {
	s := strings.ToLower(name)
	s = strings.ReplaceAll(s, "/", "-")
	s = strings.ReplaceAll(s, "_", "-")
	s = strings.ReplaceAll(s, ".", "-")
	if len(s) > 50 {
		s = s[:50]
	}
	return s
}

// specHash returns a deterministic hash for the model+settings that matter for the job.
// We intentionally don't include the raw token value — only presence/absence — to avoid storing secrets in annotations.
func specHash(md *hfopsv1alpha1.ModelDownload, model hfopsv1alpha1.ModelSpec, token string) string {
	data := map[string]interface{}{
		"model":    model,
		"settings": md.Spec.Settings,
		"tokenSet": token != "", // only whether token exists
		"pvc":      md.Spec.StoragePVC,
		"nodepool": md.Spec.NodePool,
	}
	raw, _ := json.Marshal(data)
	sum := sha1.Sum(raw)
	return hex.EncodeToString(sum[:])
}

func appendIfMissing(list []string, v string) []string {
	for _, x := range list {
		if x == v {
			return list
		}
	}
	return append(list, v)
}

// Reconcile implements the main operator logic.
func (r *ModelDownloadReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := logf.FromContext(ctx)

	var md hfopsv1alpha1.ModelDownload
	if err := r.Get(ctx, req.NamespacedName, &md); err != nil {
		if apierrors.IsNotFound(err) {
			// CR deleted
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	logger.Info("Reconciling ModelDownload", "name", md.Name)

	// Initialize status arrays if nil
	if md.Status.Pending == nil {
		md.Status.Pending = []string{}
	}
	if md.Status.Completed == nil {
		md.Status.Completed = []string{}
	}
	if md.Status.Failed == nil {
		md.Status.Failed = []string{}
	}

	// Load HF token from SecretRef (if provided)
	var token string
	if md.Spec.HFTokenRef != nil && md.Spec.HFTokenRef.Name != "" {
		var sec corev1.Secret
		if err := r.Get(ctx, types.NamespacedName{
			Namespace: md.Namespace,
			Name:      md.Spec.HFTokenRef.Name,
		}, &sec); err != nil {
			// If secret is missing, requeue with backoff
			if apierrors.IsNotFound(err) {
				logger.Info("Secret referenced by CR not found, requeueing", "secret", md.Spec.HFTokenRef.Name)
				return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
			}
			return ctrl.Result{}, err
		}
		// If the specified key doesn't exist, treat as empty and requeue
		if val, ok := sec.Data[md.Spec.HFTokenRef.Key]; ok {
			token = string(val)
		} else {
			logger.Info("Secret key not found in secret", "secret", md.Spec.HFTokenRef.Name, "key", md.Spec.HFTokenRef.Key)
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
		}
	}

	// Build quick sets for status
	completed := map[string]bool{}
	failed := map[string]bool{}
	for _, c := range md.Status.Completed {
		completed[c] = true
	}
	for _, f := range md.Status.Failed {
		failed[f] = true
	}

	// For each model in spec, ensure a Job exists (or is recreated when needed)
	for _, model := range md.Spec.Models {
		modelName := model.Name
		jobName := fmt.Sprintf("hf-dl-%s-%s", sanitize(md.Name), sanitize(modelName))
		desiredHash := specHash(&md, model, token)

		var job batchv1.Job
		err := r.Get(ctx, types.NamespacedName{Namespace: md.Namespace, Name: jobName}, &job)
		if err == nil {
			// Job exists: check annotation hash and status
			existingHash := ""
			if job.Annotations != nil {
				existingHash = job.Annotations["hfops.kamal.dev/spec-hash"]
			}

			// If spec changed or force reload requested, delete and recreate job
			if existingHash != desiredHash || md.Spec.Settings.ForceReload {
				logger.Info("Spec changed or forceReload set, recreating Job", "job", jobName, "model", modelName)
				// Delete existing job (ignore error)
				_ = r.Delete(ctx, &job)
				newJob := r.buildJob(&md, model, jobName, token, desiredHash)
				if err := ctrl.SetControllerReference(&md, &newJob, r.Scheme); err != nil {
					return ctrl.Result{}, err
				}
				if err := r.Create(ctx, &newJob); err != nil {
					return ctrl.Result{}, err
				}
				// continue to next model
				continue
			}

			// update status based on Job
			if job.Status.Succeeded > 0 {
				md.Status.Completed = appendIfMissing(md.Status.Completed, modelName)
			} else if job.Status.Failed > 0 {
				md.Status.Failed = appendIfMissing(md.Status.Failed, modelName)
			}

			// nothing else to do for this model
			continue
		}

		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}

		// Job does not exist -> create it
		logger.Info("Creating download Job", "job", jobName, "model", modelName)
		newJob := r.buildJob(&md, model, jobName, token, desiredHash)
		if err := ctrl.SetControllerReference(&md, &newJob, r.Scheme); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.Create(ctx, &newJob); err != nil {
			return ctrl.Result{}, err
		}
		md.Status.Pending = appendIfMissing(md.Status.Pending, modelName)
	}

	// Persist status updates
	if err := r.Status().Update(ctx, &md); err != nil {
		return ctrl.Result{}, err
	}

	// Requeue to refresh job statuses periodically
	return ctrl.Result{RequeueAfter: 20 * time.Second}, nil
}

// buildJob constructs the batchv1.Job for downloading the model.
func (r *ModelDownloadReconciler) buildJob(md *hfopsv1alpha1.ModelDownload, model hfopsv1alpha1.ModelSpec, jobName string, token string, hash string) batchv1.Job {
	// Keep TTL default if not set
	var ttl int32 = 3600
	if md.Spec.Settings.KeepAliveSeconds > 0 {
		ttl = md.Spec.Settings.KeepAliveSeconds
	}
	backoff := int32(0)

	cpu := md.Spec.Settings.CPU
	if cpu == "" {
		cpu = "1"
	}
	mem := md.Spec.Settings.Memory
	if mem == "" {
		mem = "4Gi"
	}

	// Build the script that the Job container will run.
	// NOTE: token is injected directly in the script env; token is not stored in annotations.
	script := fmt.Sprintf(`
cat << 'EOF' > /tmp/download.sh
%s
EOF

chmod +x /tmp/download.sh
/tmp/download.sh "%s" "%s" "%v"
`,
    downloadScript,          // 1 — the embedded script contents
    token,                   // 2 — HF token
    model.Name,              // 3 — model name
    md.Spec.Settings.EnableHFTransfer, // 4 — bool
)


	// Pod spec
	podSpec := corev1.PodSpec{
		RestartPolicy: corev1.RestartPolicyNever,
		NodeSelector:  map[string]string{"karpenter.sh/nodepool": md.Spec.NodePool},
		Volumes: []corev1.Volume{
			{
				Name: "models",
				VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
						ClaimName: md.Spec.StoragePVC,
					},
				},
			},
		},
		Containers: []corev1.Container{
			{
				Name:    "downloader",
				Image:   "ubuntu:22.04",
				Command: []string{"/bin/bash", "-c", script},
				VolumeMounts: []corev1.VolumeMount{
					{
						Name:      "models",
						MountPath: "/models",
					},
				},
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse(cpu),
						corev1.ResourceMemory: resource.MustParse(mem),
					},
					Limits: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse(cpu),
						corev1.ResourceMemory: resource.MustParse(mem),
					},
				},
			},
		},
	}

	// Construct Job
	job := batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: md.Namespace,
			Annotations: map[string]string{
				"hfops.kamal.dev/spec-hash": hash,
			},
			Labels: map[string]string{
				"app":    "hf-downloader",
				"parent": md.Name,
				"model":  sanitize(model.Name),
			},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            &backoff,
			TTLSecondsAfterFinished: &ttl,
			Template: corev1.PodTemplateSpec{
				Spec: podSpec,
			},
		},
	}
	return job
}

// SetupWithManager wires the controller with the manager and adds watches on Secrets and Jobs
func (r *ModelDownloadReconciler) SetupWithManager(mgr ctrl.Manager) error {

	return ctrl.NewControllerManagedBy(mgr).
		For(&hfopsv1alpha1.ModelDownload{}).
		Owns(&batchv1.Job{}).
		Watches(
			&corev1.Secret{},
			handler.EnqueueRequestsFromMapFunc(
				func(ctx context.Context, obj client.Object) []reconcile.Request {

					sec, ok := obj.(*corev1.Secret)
					if !ok {
						return nil
					}

					var list hfopsv1alpha1.ModelDownloadList
					_ = mgr.GetClient().List(
						ctx,
						&list,
						client.InNamespace(sec.Namespace),
					)

					reqs := []reconcile.Request{}
					for _, md := range list.Items {
						if md.Spec.HFTokenRef != nil && md.Spec.HFTokenRef.Name == sec.Name {
							reqs = append(reqs, reconcile.Request{
								NamespacedName: types.NamespacedName{
									Namespace: md.Namespace,
									Name:      md.Name,
								},
							})
						}
					}

					return reqs
				},
			),
		).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 3,
		}).
		Complete(r)
}
