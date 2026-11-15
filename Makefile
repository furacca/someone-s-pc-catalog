.PHONY: test capture-assets prepare diff

test:
	go vet ./...
	go test ./...

capture-assets:
	./prepare.sh capture-assets

prepare:
	./prepare.sh prepare

diff:
	./prepare.sh diff
