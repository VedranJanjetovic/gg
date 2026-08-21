SHELL := /bin/bash
REPOSITORY ?= VedranJanjetovic/gg

# release: build installer artifacts for every supported platform into ./bin,
# versioned one patch above the latest gg-v* GitHub release, then print the
# exact steps to publish the release.
.PHONY: release
release:
	@set -euo pipefail; \
	latest=$$( (gh release list --repo $(REPOSITORY) --limit 100 --json tagName --jq '.[].tagName' 2>/dev/null || \
	            curl -fsSL "https://api.github.com/repos/$(REPOSITORY)/releases?per_page=100" 2>/dev/null | grep -o '"tag_name": *"gg-v[0-9][^"]*"' | cut -d'"' -f4) \
	          | grep '^gg-v' | sort -V | tail -1 || true); \
	if [[ -n "$$latest" ]]; then \
	    version=$${latest#gg-v}; \
	    major=$${version%%.*}; rest=$${version#*.}; minor=$${rest%%.*}; patch=$${rest#*.}; patch=$${patch%%[-.+]*}; \
	    next="$$major.$$minor.$$((patch + 1))"; \
	    echo "release: latest GitHub release is $$latest — building gg-v$$next"; \
	else \
	    next="0.1.0"; \
	    echo "release: no gg-v* GitHub release found — building gg-v$$next"; \
	fi; \
	rm -rf ./bin && ./build-release.sh --artifacts "$$next" ./bin; \
	echo ""; \
	echo "================================================================"; \
	echo "Release gg-v$$next built into ./bin. To publish on GitHub:"; \
	echo ""; \
	echo "  1. Tag this exact commit (artifacts embed its hash — provenance"; \
	echo "     requires the tag to point at the same HEAD):"; \
	echo "       git tag gg-v$$next && git push origin gg-v$$next"; \
	echo ""; \
	echo "  2. Create the release with all six installer assets:"; \
	echo "       gh release create gg-v$$next --repo $(REPOSITORY) \\"; \
	echo "         --title \"gg v$$next\" --generate-notes \\"; \
	echo "         bin/gg-linux-amd64.tar.gz bin/gg-linux-arm64.tar.gz \\"; \
	echo "         bin/gg-darwin-amd64.tar.gz bin/gg-darwin-arm64.tar.gz \\"; \
	echo "         bin/gg-windows-amd64.zip bin/gg-windows-arm64.zip"; \
	echo ""; \
	echo "  3. Verify: curl -fsSL https://raw.githubusercontent.com/$(REPOSITORY)/main/install.sh | bash"; \
	echo "     then 'gg version' must report $$next and the tagged commit."; \
	echo "================================================================"
