/*
 * Copyright (c) 2026 Huawei Technologies Co., Ltd.
 * openFuyao is licensed under Mulan PSL v2.
 * You can use this software according to the terms and conditions of the Mulan PSL v2.
 * You may obtain a copy of Mulan PSL v2 at:
 *          http://license.coscl.org.cn/MulanPSL2
 * THIS SOFTWARE IS PROVIDED ON AN "AS IS" BASIS, WITHOUT WARRANTIES OF ANY KIND,
 * EITHER EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO NON-INFRINGEMENT,
 * MERCHANTABILITY OR FIT FOR A PARTICULAR PURPOSE.
 * See the Mulan PSL v2 for more details.
 */

package kubeops

import (
	"context"

	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/deploymentplan"
)

// PlanDeployment evaluates a caller-supplied profile against the live objects
// visible to the configured Kubernetes reader. It does not create resources,
// reserve capacity, or authorize a later deployment.
func (r *KubernetesReader) PlanDeployment(
	ctx context.Context,
	request deploymentplan.Request,
) (deploymentplan.Plan, error) {
	return deploymentplan.Generate(ctx, r.client, request)
}
