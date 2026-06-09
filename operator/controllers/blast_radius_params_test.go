/*
 * ChatCLI - Kubernetes Operator
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package controllers

import (
	"context"
	"strings"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

func newTestBlastRadiusPredictor(objs ...client.Object) *BlastRadiusPredictor {
	cb := fake.NewClientBuilder().WithScheme(newScheme())
	if len(objs) > 0 {
		cb = cb.WithObjects(objs...)
	}
	return NewBlastRadiusPredictor(cb.Build())
}

func warningsContain(warnings []string, substr string) bool {
	for _, w := range warnings {
		if strings.Contains(w, substr) {
			return true
		}
	}
	return false
}

func TestPredictScaleImpact_ReplicasParsing(t *testing.T) {
	resource := platformv1alpha1.ResourceRef{Kind: "Deployment", Name: "web", Namespace: "default"}

	tests := []struct {
		name        string
		replicas    string
		wantWarning string
	}{
		{name: "non-numeric replicas", replicas: "not-a-number", wantWarning: "unparseable replicas"},
		// 2^32+1 wraps to 1 with a bare int32 cast; parseInt32 must
		// refuse it instead of predicting a harmless scale-to-1.
		{name: "int32 overflow", replicas: "4294967297", wantWarning: "unparseable replicas"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bp := newTestBlastRadiusPredictor(newDeployment("web", "default", 5))
			prediction := &BlastRadiusPrediction{Safe: true, RiskLevel: "low"}
			bp.predictScaleImpact(context.Background(), resource, map[string]string{"replicas": tt.replicas}, prediction)

			if !warningsContain(prediction.Warnings, tt.wantWarning) {
				t.Errorf("expected warning containing %q, got %v", tt.wantWarning, prediction.Warnings)
			}
			if !prediction.Safe || len(prediction.Blockers) != 0 {
				t.Errorf("unparseable input must not flip safety, got safe=%v blockers=%v", prediction.Safe, prediction.Blockers)
			}
		})
	}

	t.Run("scale to zero is blocked", func(t *testing.T) {
		bp := newTestBlastRadiusPredictor(newDeployment("web", "default", 5))
		prediction := &BlastRadiusPrediction{Safe: true, RiskLevel: "low"}
		bp.predictScaleImpact(context.Background(), resource, map[string]string{"replicas": "0"}, prediction)

		if prediction.Safe || len(prediction.Blockers) == 0 {
			t.Errorf("scale to 0 must be blocked, got safe=%v blockers=%v", prediction.Safe, prediction.Blockers)
		}
	})

	t.Run("missing deployment short-circuits", func(t *testing.T) {
		bp := newTestBlastRadiusPredictor()
		prediction := &BlastRadiusPrediction{Safe: true, RiskLevel: "low"}
		bp.predictScaleImpact(context.Background(), resource, map[string]string{"replicas": "3"}, prediction)

		if len(prediction.Warnings) != 0 || len(prediction.Blockers) != 0 {
			t.Errorf("expected no findings when the deployment is absent, got %+v", prediction)
		}
	})
}

func TestPredictScaleStatefulSetImpact_ReplicasParsing(t *testing.T) {
	resource := platformv1alpha1.ResourceRef{Kind: "StatefulSet", Name: "db", Namespace: "default"}

	t.Run("unparseable replicas produce a warning and stop", func(t *testing.T) {
		bp := newTestBlastRadiusPredictor(newStatefulSet("db", "default", 3))
		prediction := &BlastRadiusPrediction{Safe: true, RiskLevel: "low"}
		bp.predictScaleStatefulSetImpact(context.Background(), resource, map[string]string{"replicas": "5x"}, prediction)

		if !warningsContain(prediction.Warnings, "unparseable replicas") {
			t.Errorf("expected unparseable warning, got %v", prediction.Warnings)
		}
		if !prediction.Safe {
			t.Error("unparseable input must not flip safety")
		}
	})

	t.Run("scale down warns about ordinal removal", func(t *testing.T) {
		bp := newTestBlastRadiusPredictor(newStatefulSet("db", "default", 3))
		prediction := &BlastRadiusPrediction{Safe: true, RiskLevel: "low"}
		bp.predictScaleStatefulSetImpact(context.Background(), resource, map[string]string{"replicas": "1"}, prediction)

		if !warningsContain(prediction.Warnings, "highest ordinal") {
			t.Errorf("expected scale-down warning, got %v", prediction.Warnings)
		}
	})

	t.Run("scale to zero is blocked", func(t *testing.T) {
		bp := newTestBlastRadiusPredictor(newStatefulSet("db", "default", 3))
		prediction := &BlastRadiusPrediction{Safe: true, RiskLevel: "low"}
		bp.predictScaleStatefulSetImpact(context.Background(), resource, map[string]string{"replicas": "0"}, prediction)

		if prediction.Safe || len(prediction.Blockers) == 0 {
			t.Errorf("scale to 0 must be blocked, got safe=%v blockers=%v", prediction.Safe, prediction.Blockers)
		}
	})
}

func TestPredictHPAAdjustImpact(t *testing.T) {
	resource := platformv1alpha1.ResourceRef{Kind: "HorizontalPodAutoscaler", Name: "web-hpa", Namespace: "default"}

	tests := []struct {
		name        string
		params      map[string]string
		wantWarning string
	}{
		{
			name:        "valid maxReplicas warns about capacity",
			params:      map[string]string{"maxReplicas": "10"},
			wantWarning: "maxReplicas to 10",
		},
		{
			name:        "unparseable maxReplicas is reported",
			params:      map[string]string{"maxReplicas": "1e3"},
			wantWarning: "unparseable maxReplicas",
		},
		{
			name:   "absent maxReplicas adds nothing",
			params: map[string]string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bp := newTestBlastRadiusPredictor()
			prediction := &BlastRadiusPrediction{Safe: true, RiskLevel: "low"}
			bp.predictHPAAdjustImpact(context.Background(), resource, tt.params, prediction)

			if tt.wantWarning == "" {
				if len(prediction.Warnings) != 0 {
					t.Errorf("expected no warnings, got %v", prediction.Warnings)
				}
				return
			}
			if !warningsContain(prediction.Warnings, tt.wantWarning) {
				t.Errorf("expected warning containing %q, got %v", tt.wantWarning, prediction.Warnings)
			}
		})
	}
}
