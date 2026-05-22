# network-docker
Dockerfiles for network server applications

Apps live in the following structure on the server, with this repo being represented by the `configs` folder

```
/
└── docker/
    └── configs/
        ├── docker.sh
        ├── app1/
        │   └── docker-compose.yml
        └── app2/
            └── docker-compose.yml
```


## docker.sh

A script that takes an action (`up` or `down`) and a list of one or more services. Loops through each directory given, and performs `docker compose up -d` or `docker compose down` - depending on the action given.

