.PHONY: test test-verbose lint check

test:
	python3 -m unittest discover -s tests -t .

test-verbose:
	python3 -m unittest discover -s tests -t . -v

lint:
	ruff check app.py qr.py scripts/ tests/
	shellcheck -x install.sh uninstall.sh

check: lint test
