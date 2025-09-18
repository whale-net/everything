# Makefile for ManMan Helm Chart Testing

.PHONY: test test-chart test-pdb test-templates test-all clean help

# Default target
help:
	@echo "ManMan Helm Chart Test Suite"
	@echo ""
	@echo "Available targets:"
	@echo "  test-all      - Run all test suites"
	@echo "  test-chart    - Run main integration tests"
	@echo "  test-pdb      - Run Pod Disruption Budget tests"
	@echo "  test-templates- Run individual template tests"
	@echo "  test-render   - Quick template rendering test"
	@echo "  clean         - Clean test output directory"
	@echo "  lint          - Lint the Helm chart"
	@echo "  help          - Show this help message"

# Run all tests
test-all: test-chart test-pdb test-templates
	@echo "✅ All tests completed successfully!"

# Main integration test suite
test-chart:
	@echo "🧪 Running main chart tests..."
	./test-chart.sh

# Pod Disruption Budget specific tests
test-pdb:
	@echo "🛡️  Running PDB tests..."
	./test-pdb.sh

# Individual template tests
test-templates:
	@echo "📄 Running template unit tests..."
	./test-templates.sh

# Quick template rendering test
test-render:
	@echo "⚡ Quick rendering test..."
	helm template manman-test . -f test-values/all-enabled.yaml > /dev/null
	@echo "✅ Templates render successfully"

# Lint the chart
lint:
	@echo "🔍 Linting Helm chart..."
	helm lint .
	@echo "✅ Chart linting passed"

# Clean test outputs
clean:
	@echo "🧹 Cleaning test outputs..."
	rm -rf test-output/
	@echo "✅ Test outputs cleaned"

# Validate chart with different configurations
validate:
	@echo "✔️  Validating chart configurations..."
	@for values_file in test-values/*.yaml; do \
		echo "  Testing with $$values_file"; \
		helm template manman-test . -f "$$values_file" | kubectl --dry-run=client apply -f - > /dev/null 2>&1; \
		if [ $$? -eq 0 ]; then \
			echo "    ✅ Valid"; \
		else \
			echo "    ❌ Invalid"; \
			exit 1; \
		fi; \
	done
	@echo "✅ All configurations are valid"

# Development helpers
dev-test:
	@echo "🚀 Running development tests..."
	helm template manman-test . -f test-values/dev-ingress.yaml

prod-test:
	@echo "🏭 Running production tests..."
	helm template manman-test . -f test-values/production.yaml

# Install test dependencies (if needed)
install-deps:
	@echo "📦 Checking test dependencies..."
	@command -v helm >/dev/null 2>&1 || { echo "❌ helm is required but not installed"; exit 1; }
	@command -v kubectl >/dev/null 2>&1 || { echo "❌ kubectl is required but not installed"; exit 1; }
	@echo "✅ All dependencies are available"
