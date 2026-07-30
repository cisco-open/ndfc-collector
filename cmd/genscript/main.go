// Command genscript generates the ndfc_collector.py Python script
// from the embedded request data.

// SPDX-License-Identifier: Apache-2.0

// Copyright 2026 Cisco Systems, Inc. and their affiliates

// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at

// http://www.apache.org/licenses/LICENSE-2.0

// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"ndfc-collector/pkg/requests"
)

const pyHeader = `#!/usr/bin/env python3

# SPDX-License-Identifier: Apache-2.0

# Copyright 2026 Cisco Systems, Inc. and their affiliates

# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at

# http://www.apache.org/licenses/LICENSE-2.0

# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.
	
# NDFC Data Collector
# This script collects data from Cisco NDFC for health check analysis
# Generated from collector/pkg/requests/requests.yaml - do not edit manually

import json
import os
import re
import sys
import zipfile
from getpass import getpass

import requests
import urllib3

urllib3.disable_warnings(urllib3.exceptions.InsecureRequestWarning)

# REQUESTS contains dictionaries with these keys:
#   - url: full host-relative API path
#   - depends_on: placeholder map or None
#   - query: optional query string map with placeholder support
#   - db_key: optional storage key used to derive the output filename
#   - list_path: dot-notation path to the item array in the response, used to
#     unwrap wrapped responses (e.g. {"fabrics": [...]}) before extracting
#     placeholder values for dependent requests. "" means the response is
#     already a single object; "@this" means the response is already a list.
# Generated from collector/pkg/requests/requests.yaml - do not edit the list below manually.
`

const pyFooter = `

class NDFCClient:
    def __init__(self, host, username, password):
        self.host = host
        self.username = username
        self.password = password
        self.session = requests.Session()
        self.base_url = f"https://{host}"

    def login(self):
        login_url = f"{self.base_url}/login"
        payload = {
            "userName": self.username,
            "userPasswd": self.password,
            "domain": "DefaultAuth",
        }
        try:
            response = self.session.post(login_url, json=payload, verify=False)
            response.raise_for_status()
            print(f"Successfully authenticated to NDFC at {self.host}")
            return True
        except requests.exceptions.RequestException as e:
            print(f"Authentication failed: {e}")
            return False

    def get(self, endpoint, params=None):
        url = f"{self.base_url}{endpoint}"
        try:
            response = self.session.get(url, params=params, verify=False)
            response.raise_for_status()
            return response.json()
        except requests.exceptions.RequestException as e:
            print(f"Request failed for {endpoint}: {e}")
            return None


def url_to_filename(url):
    filename = url.strip('/').replace('/', '.')
    return f"{filename}.json"


def db_key_to_filename(db_key):
    if not db_key:
        return None
    return f"{db_key.replace('/', '.')}.json"


def substitute_url(template, context):
    def replace(match):
        key = match.group(1)
        return str(context.get(key, match.group(0)))
    return re.sub(r'\{([^}]+)\}', replace, template)


def substitute_query(query_template, context):
    if not query_template:
        return None
    return {key: substitute_url(value, context) for key, value in query_template.items()}


def extract_list_result(result, list_path):
    if not list_path or list_path == '@this':
        return result
    if isinstance(result, dict) and isinstance(result.get(list_path), list):
        return result[list_path]
    return result


def extract_ctx(item, parent_ctx, key_mappings):
    ctx = dict(parent_ctx or {})
    if isinstance(item, dict):
        for placeholder, key in key_mappings.items():
            if key in item and not isinstance(item[key], (dict, list)):
                ctx[placeholder] = str(item[key])
    return ctx


def build_levels(request_defs):
    by_url = {req['url']: req.get('depends_on') for req in request_defs}
    depth = {}

    def calc_depth(url):
        if url in depth:
            return depth[url]
        depends_on = by_url.get(url)
        if depends_on is None:
            depth[url] = 0
        else:
            parent_urls = {dep['url'] for dep in depends_on.values()}
            max_parent = max(calc_depth(parent_url) for parent_url in parent_urls)
            depth[url] = max_parent + 1
        return depth[url]

    for req in request_defs:
        calc_depth(req['url'])

    max_depth = max(depth.values()) if depth else 0
    levels = [[] for _ in range(max_depth + 1)]
    for req in request_defs:
        levels[depth[req['url']]].append(req)
    return levels


def _cartesian(groups):
    if not groups:
        yield []
        return
    for item in groups[0]:
        for rest in _cartesian(groups[1:]):
            yield [item] + rest


def collect_data(client):
    data = {}
    results = {}

    levels = build_levels(REQUESTS)

    for level_reqs in levels:
        for request_def in level_reqs:
            url_template = request_def['url']
            depends_on = request_def.get('depends_on')
            query_template = request_def.get('query')
            db_key_template = request_def.get('db_key')
            list_path = request_def.get('list_path')

            if depends_on is None:
                filename = db_key_to_filename(db_key_template) or url_to_filename(url_template)
                print(f"Fetching {filename}...")
                result = client.get(url_template, params=substitute_query(query_template, {}))
                if result is not None:
                    data[filename] = result
                    results[url_template] = [({}, extract_list_result(result, list_path))]
                    print(f"  ✓ {filename} complete")
                else:
                    print(f"  ✗ {filename} failed")
            else:
                by_parent_url = {}
                for placeholder, dep in depends_on.items():
                    parent_url = dep['url']
                    key = dep['key']
                    by_parent_url.setdefault(parent_url, {})[placeholder] = key

                groups = []
                for parent_url, key_mappings in by_parent_url.items():
                    ctxs = []
                    for parent_ctx, parent_data in results.get(parent_url, []):
                        items = parent_data if isinstance(parent_data, list) else [parent_data]
                        for item in items:
                            ctxs.append(extract_ctx(item, parent_ctx, key_mappings))
                    groups.append(ctxs)

                level_results = []
                for combo in _cartesian(groups):
                    merged_ctx = {}
                    for ctx in combo:
                        merged_ctx.update(ctx)
                    resolved_url = substitute_url(url_template, merged_ctx)
                    resolved_query = substitute_query(query_template, merged_ctx)
                    resolved_db_key = substitute_url(db_key_template, merged_ctx) if db_key_template else None
                    filename = db_key_to_filename(resolved_db_key) or url_to_filename(resolved_url)
                    print(f"Fetching {filename}...")
                    result = client.get(resolved_url, params=resolved_query)
                    if result is not None:
                        data[filename] = result
                        level_results.append((merged_ctx, extract_list_result(result, list_path)))
                        print(f"  ✓ {filename} complete")
                    else:
                        print(f"  ✗ {filename} failed")
                if level_results:
                    results[url_template] = level_results

    return data


def main():
    print("NDFC Data Collector")
    print("=" * 50)
    print()

    host = input("NDFC hostname or IP: ").strip()
    username = input("NDFC username: ").strip()
    password = getpass("NDFC password: ")

    client = NDFCClient(host, username, password)
    if not client.login():
        sys.exit(1)

    print()
    print("Collecting data...")
    data = collect_data(client)

    output_file = "ndfc-collection-data.zip"
    print()
    print(f"Creating {output_file}...")

    with zipfile.ZipFile(output_file, 'w', zipfile.ZIP_DEFLATED) as zipf:
        for filename, content in data.items():
            zipf.writestr(filename, json.dumps(content, indent=2))

    print()
    print("=" * 50)
    print("Collection complete!")
    print(f"Output written to {os.path.abspath(output_file)}")
    print("Please provide this file to Cisco Services for analysis.")


if __name__ == "__main__":
    main()
`

func main() {
	scriptPath := "ndfc_collector.py"
	if _, err := os.Stat("../../go.mod"); err == nil {
		scriptPath = "../../ndfc_collector.py"
	}

	absPath, err := filepath.Abs(scriptPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error resolving path: %v\n", err)
		os.Exit(1)
	}

	f, err := os.Create(absPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating file: %v\n", err)
		os.Exit(1)
	}
	defer f.Close()

	reqs, err := requests.GetRequests()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error getting requests: %v\n", err)
		os.Exit(1)
	}

	fmt.Fprint(f, pyHeader)
	fmt.Fprintln(f, "REQUESTS = [")
	for _, r := range reqs {
		fmt.Fprintln(f, "    {")
		fmt.Fprintf(f, "        \"url\": %q,\n", r.URL)
		if len(r.DependsOn) == 0 {
			fmt.Fprintln(f, "        \"depends_on\": None,")
		} else {
			depKeys := make([]string, 0, len(r.DependsOn))
			for k := range r.DependsOn {
				depKeys = append(depKeys, k)
			}
			sort.Strings(depKeys)
			fmt.Fprintln(f, "        \"depends_on\": {")
			for i, placeholder := range depKeys {
				dep := r.DependsOn[placeholder]
				suffix := ","
				if i == len(depKeys)-1 {
					suffix = ""
				}
				fmt.Fprintf(f, "            %q: {\"url\": %q, \"key\": %q}%s\n", placeholder, dep.URL, dep.Key, suffix)
			}
			fmt.Fprintln(f, "        },")
		}
		if len(r.Query) == 0 {
			fmt.Fprintln(f, "        \"query\": None,")
		} else {
			queryKeys := make([]string, 0, len(r.Query))
			for k := range r.Query {
				queryKeys = append(queryKeys, k)
			}
			sort.Strings(queryKeys)
			fmt.Fprintln(f, "        \"query\": {")
			for i, key := range queryKeys {
				suffix := ","
				if i == len(queryKeys)-1 {
					suffix = ""
				}
				fmt.Fprintf(f, "            %q: %q%s\n", key, r.Query[key], suffix)
			}
			fmt.Fprintln(f, "        },")
		}
		fmt.Fprintf(f, "        \"db_key\": %q,\n", r.DBKey)
		fmt.Fprintf(f, "        \"list_path\": %q,\n", r.ListPath)
		fmt.Fprintln(f, "    },")
	}
	fmt.Fprint(f, "]")
	fmt.Fprint(f, pyFooter)

	if err := os.Chmod(absPath, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "Error making script executable: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Generated %s successfully\n", absPath)
}
