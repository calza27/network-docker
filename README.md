# network-docker
Dockerfiles for network server applications

Apps will live in the following structure on the server, with this repo being represented by the `configs` folder

```
/
└── docker/
    └── configs/
        ├── docker-up.sh
        ├── docker-down.sh
        ├── app1/
        │   └── docker-compose.yml
        └── app2/
            └── docker-compose.yml
```


## docker-up.sh

A simple script that loops through each directory containing a `docker-compose.yml` file and performs `docker compose up -d`


## docker-down.sh

A simple script that loops through each directory containing a `docker-compose.yml` file and performs `docker compose down`
