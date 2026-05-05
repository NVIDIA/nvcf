Notes:
- Multi instances of the same container will be spun up. 
- One must realize it is the "head" node and run the main server entrypoint.
- The rest of the nodes only require running a health/readiness endpoint as defined in the helm chart
- The main head node will run `mpirun` and connect to the other nodes to coordinate.
- $HOSTFILE contains the names of the other replicas (including the head node) for use with `mpirun`