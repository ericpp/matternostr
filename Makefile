.PHONY: docker matternostr upload

all: matternostr

docker:
	docker build -t ericpp/matternostr --load .

matternostr:
	CGO_ENABLED=0 go build

upload:
	docker push ericpp/matternostr
