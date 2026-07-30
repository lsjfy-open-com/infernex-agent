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

package deployer

import "context"

const TinyModelCatalogID = "smollm2-135m-q4"

// Deployer is the narrow mutation boundary exposed to the MCP server. It does
// not accept images, commands, URLs, or arbitrary Kubernetes objects.
type Deployer interface {
	Deploy(context.Context, Request) (Result, error)
	Delete(context.Context, Request) (Result, error)
}

type Request struct {
	Namespace string
	Name      string
	CatalogID string
	Confirm   bool
}

type Result struct {
	Namespace    string `json:"namespace"`
	Name         string `json:"name"`
	CatalogID    string `json:"catalogId"`
	Operation    string `json:"operation"`
	ResourceKind string `json:"resourceKind"`
	Endpoint     string `json:"endpoint,omitempty"`
	InferenceAPI string `json:"inferenceApi,omitempty"`
}
