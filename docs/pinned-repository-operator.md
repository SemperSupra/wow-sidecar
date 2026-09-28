# Pinned repository operator

This optional integration primitive replaces the historical hard-coded GARM handler with a reusable bounded pattern.

A local registry may configure exactly:
- one operator profile;
- one repository;
- one exact 40-hex revision;
- one normalized relative script path;
- an optional fixed environment;
- whether canonical `main` must still equal the admitted revision.

The durable request cannot supply or override any of those values and cannot supply command arguments. The operator therefore does not create a generic remote shell.

The worker remains responsible for canonical authority binding. This integration is only the locally admitted actuator associated with a profile.

The current GARM capacity-two integration can be represented as local configuration of this primitive, while GARM/Foundry policy and authority remain in their owning repositories.
