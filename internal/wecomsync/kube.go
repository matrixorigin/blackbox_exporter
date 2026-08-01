// Copyright The Prometheus Authors
// Licensed under the Apache License, Version 2.0 (the "License");

package wecomsync

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

var probeGVR = schema.GroupVersionResource{
	Group:    "monitoring.coreos.com",
	Version:  "v1",
	Resource: "probes",
}

type KubernetesProbeStore struct {
	client    dynamic.Interface
	cfg       Config
	namespace string
}

func NewKubernetesProbeStore(cfg Config) (*KubernetesProbeStore, error) {
	restConfig, err := KubernetesRESTConfig(cfg.Kubernetes.Kubeconfig)
	if err != nil {
		return nil, err
	}
	client, err := dynamic.NewForConfig(restConfig)
	if err != nil {
		return nil, err
	}
	return &KubernetesProbeStore{
		client:    client,
		cfg:       cfg,
		namespace: cfg.Kubernetes.Namespace,
	}, nil
}

func KubernetesRESTConfig(kubeconfig string) (*rest.Config, error) {
	if kubeconfig == "" {
		if env := os.Getenv("KUBECONFIG"); env != "" {
			kubeconfig = env
		}
	}
	if kubeconfig != "" {
		return clientcmd.BuildConfigFromFlags("", kubeconfig)
	}
	if cfg, err := rest.InClusterConfig(); err == nil {
		return cfg, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return clientcmd.BuildConfigFromFlags("", filepath.Join(home, ".kube", "config"))
}

func (s *KubernetesProbeStore) Apply(ctx context.Context, probe ProbeSpec) error {
	obj := ProbeObject(probe, s.cfg)
	_, err := s.client.Resource(probeGVR).Namespace(s.namespace).Apply(
		ctx,
		probe.Name,
		obj,
		metav1.ApplyOptions{
			FieldManager: s.cfg.Kubernetes.FieldManager,
			Force:        true,
		},
	)
	return err
}

func (s *KubernetesProbeStore) ListManaged(ctx context.Context) ([]string, error) {
	selector := fmt.Sprintf("%s=%s", managedByLabel, s.cfg.Kubernetes.FieldManager)
	items, err := s.client.Resource(probeGVR).Namespace(s.namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(items.Items))
	for _, item := range items.Items {
		names = append(names, item.GetName())
	}
	return names, nil
}

func (s *KubernetesProbeStore) Delete(ctx context.Context, name string) error {
	propagation := metav1.DeletePropagationBackground
	err := s.client.Resource(probeGVR).Namespace(s.namespace).Delete(
		ctx,
		name,
		metav1.DeleteOptions{
			PropagationPolicy: &propagation,
		},
	)
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

type DryRunProbeStore struct {
	Config  Config
	Applied []string
	Deleted []string
}

func (s *DryRunProbeStore) Apply(_ context.Context, probe ProbeSpec) error {
	s.Applied = append(s.Applied, probe.Name)
	return nil
}

func (s *DryRunProbeStore) ListManaged(context.Context) ([]string, error) {
	return nil, nil
}

func (s *DryRunProbeStore) Delete(_ context.Context, name string) error {
	s.Deleted = append(s.Deleted, name)
	return nil
}
