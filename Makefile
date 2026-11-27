GO ?= go
GOIMPORTS ?= goimports
GOLANGCI_LINT ?= golangci-lint
GOVULNCHECK ?= govulncheck
PKGS := ./...

.PHONY: all build test vet clean docs-check fmt-check lint vuln dod

all: build test

build:
	$(GO) build ./...

test:
	$(GO) test -race -count=1 $(PKGS)

vet:
	$(GO) vet $(PKGS)

fmt-check:
	@if command -v $(GOIMPORTS) >/dev/null 2>&1; then \
	  out="$$($(GOIMPORTS) -l .)"; \
	else \
	  echo "note: $(GOIMPORTS) not found; using gofmt -l ." >&2; \
	  out="$$(gofmt -l .)"; \
	fi; \
	if [ -n "$$out" ]; then \
	  echo "unformatted files:" >&2; echo "$$out" >&2; exit 1; \
	fi

lint:
	@command -v $(GOLANGCI_LINT) >/dev/null 2>&1 || { \
	  echo "missing $(GOLANGCI_LINT); install it or set GOLANGCI_LINT" >&2; exit 1; \
	}
	$(GOLANGCI_LINT) run

vuln:
	@command -v $(GOVULNCHECK) >/dev/null 2>&1 || { \
	  echo "missing $(GOVULNCHECK); install it or set GOVULNCHECK" >&2; exit 1; \
	}
	$(GOVULNCHECK) $(PKGS)

dod: docs-check fmt-check vet lint test build vuln
	@echo "Automated definition-of-done checks passed."
	@echo "Manual checks still apply: requested behavior, clean cutover, edge"
	@echo "cases, real-provider smokes, and the public API surface."
	@echo "See docs/definition-of-done.md."

# docs-check fails on a relative link whose file is missing and on a #fragment
# that no heading on the target page produces. The heading slug follows
# GitHub: lowercase, drop everything but letters, digits, space, hyphen, and
# underscore, then turn spaces into hyphens.
docs-check:
	@status=0; \
	for f in README.md AGENTS.md $$(find docs -type f -name '*.md' | sort); do \
	  dir=$$(dirname "$$f"); \
	  awk '/^[[:space:]]*(```|~~~)/{fence=!fence; next} !fence' "$$f" | \
	  sed -E 's/`[^`]*`//g' | \
	  grep -oE '!?\[[^]]*\]\([^)]+\)' | \
	  sed -E 's/^!?\[[^]]*\]\(//; s/\)$$//' | \
	  while IFS= read -r link; do \
	    case "$$link" in http://*|https://*|mailto:*) continue;; esac; \
	    anchor=; \
	    case "$$link" in \
	      '#'*) target="$$f"; path="$$f"; anchor=$${link#'#'};; \
	      *'#'*) target=$${link%%#*}; target=$${target%% *}; path="$$dir/$$target"; anchor=$${link#*#};; \
	      *) target=$${link%% *}; path="$$dir/$$target";; \
	    esac; \
	    anchor=$${anchor%% *}; \
	    if [ -z "$$target" ] || [ ! -e "$$path" ]; then \
	      echo "$$f: broken link: $$link"; exit 1; \
	    fi; \
	    if [ -n "$$anchor" ]; then \
	      if ! awk '/^[[:space:]]*(```|~~~)/{fence=!fence; next} !fence' "$$path" | \
	        grep -E '^#{1,6}[[:space:]]' | \
	        sed -E 's/^#{1,6}[[:space:]]+//; s/[[:space:]]+$$//' | \
	        tr 'A-Z' 'a-z' | \
	        sed -E 's/[^a-z0-9 _-]//g; s/ /-/g' | \
	        grep -qxF "$$anchor"; then \
	        echo "$$f: broken anchor: $$link (no such heading in $$path)"; exit 1; \
	      fi; \
	    fi; \
	  done || status=1; \
	done; \
	exit $$status

clean:
	rm -rf bin
