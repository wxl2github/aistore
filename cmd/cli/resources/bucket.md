This section lists operations on *buckets*. For types of supported buckets (AIS, Cloud, backend, etc.) and many more examples, please refer to [buckets in-depth overview](/docs/bucket.md).

## Create bucket

`ais create bucket BUCKET_NAME [BUCKET_NAME...]`

Create an ais bucket or buckets.

### Examples

#### Create AIS bucket

Create buckets `bucket_name1` and `bucket_name2`, both with AIS provider.
`bucket_name2`'s provider by default is set to `ais://`, see [backend provider info](../README.md#bucket-provider).

```console
$ ais create bucket ais://bucket_name1 bucket_name2
"ais://bucket_name1" bucket created
"bucket_name2" bucket created
```

#### Create AIS bucket in local namespace

Create bucket `bucket_name` in `ml` namespace.

```console
$ ais create bucket ais://#ml/bucket_name
"ais://#ml/bucket_name" bucket created
```

#### Create bucket in remote AIS cluster

Create bucket `bucket_name` in global namespace of AIS remote cluster with `Bghort1l` UUID.

```console
$ ais create bucket ais://@Bghort1l/bucket_name
"ais://@Bghort1l/bucket_name" bucket created
```

Create bucket `bucket_name` in `ml` namespace of AIS remote cluster with `Bghort1l` UUID.

```console
$ ais create bucket ais://@Bghort1l#ml/bucket_name
"ais://@Bghort1l#ml/bucket_name" bucket created
```
#### Create bucket with custom properties

Create bucket `bucket_name` with custom properties specified.

```console
#Key-value format
$ ais create bucket ais://@Bghort1l/bucket_name --bucket-props="mirror.enabled=true mirror.copies=2"
"ais://@Bghort1l/bucket_name" bucket created

#JSON format
$ ais create bucket ais://@Bghort1l/bucket_name --bucket-props='{"versioning": {"enabled": true, "validate_warm_get": true}}'
"ais://@Bghort1l/bucket_name" bucket created
```

#### Incorrect buckets creation

```console
$ ais create bucket aws://bucket_name
Creating cloud buckets (aws://bucket_name) is not supported
```

## Delete bucket

`ais rm bucket BUCKET [BUCKET...]`

Delete an ais bucket or buckets.

### Examples

#### Remove local buckets

Remove local buckets `bucket_name1` and `bucket_name2`.

```console
$ ais rm bucket ais://bucket_name1 bucket_name2
"ais://bucket_name1" bucket destroyed
"bucket_name2" bucket destroyed
```

#### Remove local bucket in local namespace

Remove bucket `bucket_name` from `ml` namespace.

```console
$ ais rm bucket ais://#ml/bucket_name
"ais://#ml/bucket_name" bucket destroyed
```

#### Remove bucket in remote AIS cluster

Remove bucket `bucket_name` from global namespace of AIS remote cluster with `Bghort1l` UUID.

```console
$ ais rm bucket ais://@Bghort1l/bucket_name
"ais://@Bghort1l/bucket_name" bucket destroyed
```

Remove bucket `bucket_name` from `ml` namespace of AIS remote cluster with `Bghort1l` UUID.

```console
$ ais rm bucket ais://@Bghort1l#ml/bucket_name
"ais://@Bghort1l#ml/bucket_name" bucket destroyed
```

#### Incorrect buckets removal

Removing cloud buckets is not supported.

```console
$ ais rm bucket aws://bucket_name
Removing cloud buckets (aws://bucket_name) is not supported
```

## List bucket names

`ais ls`

List all bucket names.

`ais ls --regex "ngn*"`

List all bucket names matching the `ngn*` regex expression.

`ais ls aws://` or `ais ls ais://`

List all bucket names for the specific provider.

`ais ls ais://#name`

List all bucket names for the `ais` provider and `name` namespace.

`ais ls ais://@uuid#namespace`

List all bucket names for the `ais` provider and `uuid#namespace` namespace.
`uuid` should be equal to remote cluster UUID and `namespace` is optional name of the remote namespace (if `namespace` not provided the global namespace will be used).

### Options

| Name | Type | Description | Default |
| --- | --- | --- | --- |
| `--regex` | `string` | Pattern for matching bucket names | `""` |
| `--no-headers` | `bool` | Display tables without headers | `false` |

## List object names

`ais ls BUCKET_NAME`

List all objects contained in `BUCKET_NAME` bucket.

### Options

| Name | Type | Description | Default |
| --- | --- | --- | --- |
| `--regex` | `string` | Pattern for matching object names | `""` |
| `--template` | `string` | Template for matching object names | `""` |
| `--prefix` | `string` | Prefix for matching object names | `""` |
| `--paged` | `bool` | Fetch and print objects page by page | `false` |
| `--max-pages` | `int` | Max. number of pages to list | `0` |
| `--page-size` | `int` | Max. number of object names per page | `1000` |
| `--props` | `string` | Comma-separated properties to return with object names | `"size,version"`
| `--limit` | `int` | Max. number of object names to list | `0` |
| `--show-unmatched` | `bool` | List objects unmatched by regex and template as well, after the matched ones | `false` |
| `--all` | `bool` | Show all objects, including misplaced, duplicated, etc. | `false` |
| `--marker` | `string` | Start listing objects starting from the object that follows the marker alphabetically | `""` |
| `--no-headers` | `bool` | Display tables without headers | `false` |
| `--cached` | `bool` | For a cloud bucket, shows only objects that have already been downloaded and are cached on local drives (ignored for ais buckets) | `false` |
| `--use-cache` | `bool` | Use proxy cache to speed up list object request | `false` |
| `--start-after` | `string` | Object name after which the listing should start | `""` |

### Examples

#### With provider auto-detection

List object names in the bucket `bucket_name`.
Backend provider is auto-detected.

```console
$ ais ls bucket_name
NAME		SIZE		VERSION
shard-0.tar	16.00KiB	1
shard-1.tar	16.00KiB	1
shard-10.tar	16.00KiB	1
shard-2.tar	16.00KiB	1
shard-3.tar	16.00KiB	1
shard-4.tar	16.00KiB	1
shard-5.tar	16.00KiB	1
shard-6.tar	16.00KiB	1
shard-7.tar	16.00KiB	1
shard-8.tar	16.00KiB	1
shard-9.tar	16.00KiB	1
```

#### From the specific provider

List objects in the AIS bucket `bucket_name`.

```console
$ ais ls ais://bucket_name
NAME		SIZE		VERSION
shard-0.tar	16.00KiB	1
shard-1.tar	16.00KiB	1
...
```

List objects in the cloud bucket `bucket_name`.

```console
ais ls aws://bucket_name
NAME		SIZE		VERSION
shard-0.tar	16.00KiB	1
shard-1.tar	16.00KiB	1
...
```

#### From AIS remote cluster with specific namespace

List objects in the bucket `bucket_name` and `ml` namespace contained on AIS remote cluster with `Bghort1l` UUID.

```console
$ ais ls ais://@Bghort1l#ml/bucket_name
NAME		SIZE		VERSION
shard-0.tar	16.00KiB	1
shard-1.tar	16.00KiB	1
...
```

#### With prefix

List objects which match given prefix.

```console
$ ais ls ais://bucket_name --prefix "shard-1"
NAME		SIZE		VERSION
shard-1.tar	16.00KiB	1
shard-10.tar	16.00KiB	1
```

#### [experimental] Using proxy cache

Experimental support for the proxy's cache can be enabled with `--use-cache` option.
In such case the proxy will cache list object request, so the subsequent calls will be faster.

```console
$ ais ls ais://bucket_name --use-cache
NAME		SIZE		VERSION
shard-0.tar	16.00KiB	1
shard-1.tar	16.00KiB	1
...
```

## Evict cloud bucket

`ais evict BUCKET_NAME`

Evict a cloud bucket. It also resets the properties of the bucket (if changed).

## Move or Rename a bucket

`ais mv bucket BUCKET_NAME NEW_BUCKET_NAME`

Move (ie. rename) an AIS bucket. If the `NEW_BUCKET_NAME` already exists, the `mv` operation will not proceed.


> Cloud bucket move is not supported.

### Examples

#### Move AIS bucket

Move AIS bucket `bucket_name` to AIS bucket `new_bucket_name`.

```console
$ ais mv bucket bucket_name new_bucket_name
Moving bucket "bucket_name" to "new_bucket_name" in progress.
To check the status, run: ais show xaction mvlb new_bucket_name
```

## Copy bucket

`ais cp bucket SRC_BUCKET_NAME DST_BUCKET_NAME`

Copy an existing bucket to a new bucket. If destination bucket is a cloud bucket it has to exist.

### Options
| Name | Type | Description | Default |
| --- | --- | --- | --- |
| `--dry-run` | `bool` | Don't actually copy bucket, only include stats what would happen | `false` |
| `--prefix` | `string` | Prefix added to every new object's name | `""` |
| `--wait` | `bool` | Wait until copying of a bucket is finished | `false` |

### Examples

#### Copy AIS bucket

Copy AIS bucket `src_bucket` to AIS bucket `dst_bucket`.

```console
$ ais cp bucket ais://src_bucket ais://dst_bucket
Copying bucket "ais://bucket_name" to "ais://dst_bucket" in progress.
To check the status, run: ais show xaction copybck ais://dst_bucket
```

#### Copy AIS bucket and wait until finish

The same as above, but wait until copying is finished.

```console
$ ais cp bucket ais://src_bucket ais://dst_bucket --wait
```

#### Copy cloud bucket to another cloud bucket

Copy AWS bucket `src_bucket` to AWS bucket `dst_bucket`.

```console
# Make sure that both buckets exist.
$ ais ls aws://
AWS Buckets (2)
  aws://src_bucket
  aws://dst_bucket
$ ais cp bucket aws://src_bucket aws://dst_bucket
Copying bucket "aws://src_bucket" to "aws://dst_bucket" in progress.
To check the status, run: ais show xaction copybck aws://dst_bucket
```

## Show bucket summary

`ais show bucket [BUCKET_NAME]`

Show aggregated information about objects in the bucket `BUCKET_NAME`.
If `BUCKET_NAME` is omitted, shows information about all buckets.

### Options

| Flag | Type | Description | Default |
| --- | --- | --- | --- |
| `--fast` | `bool` | Enforce using faster methods to find out the buckets' details. The output may not be accurate. | `false`

## Make N copies

`ais set-copies BUCKET_NAME --copies <value>`

Start an extended action to bring a given bucket to a certain redundancy level (`value` copies). Read more about this feature [here](../../../docs/storage_svcs.md#n-way-mirror).

### Options

| Flag | Type | Description | Default |
| --- | --- | --- | --- |
| `--copies` | `int` | Number of copies | `1` |

## Make all objects erasure coded

`ais ec-encode BUCKET_NAME --data-slices <value> --parity-slices <value>`

Start an extended action that enables data protection for a given bucket and encodes all its objects.
Erasure coding must be disabled for the bucket prior to running `ec-encode` extended action.
Read more about this feature [here](../../../docs/storage_svcs.md#erasure-coding).

### Options

| Flag | Type | Description |
| --- | --- | --- |
| `--data-slices`, `--data`, `-d` | `int` | Number of data slices |
| `--parity-slices`, `--parity`, `-p` | `int` | Number of parity slices |

All options are required and must be greater than `0`.

## Show bucket props

`ais show props BUCKET_NAME [PROP_PREFIX]`

List [properties](../../../docs/bucket.md#properties-and-options) of the bucket.
By default, condensed form of bucket props sections is presented.

When `PROP_PREFIX` is set, only props that start with `PROP_PREFIX` will be displayed.
Useful `PROP_PREFIX` are: `access, checksum, ec, lru, mirror, provider, versioning`.

### Options

| Flag | Type | Description | Default |
| --- | --- | --- | --- |
| `--json` | `bool` | Output in JSON format | `false` |
| `-v` | `bool` | Show list of properties with full names | `false` |

### Examples

#### Show bucket props with provided section

Show only `lru` section of bucket props for `bucket_name` bucket.

```console
$ ais show props bucket_name
PROPERTY	 VALUE
access		 GET,PUT,DELETE,HEAD,ColdGET
checksum	 Type: xxhash | Validate: ColdGET
created		2020-04-08T16:20:12-08:00
ec		 Disabled
lru		 Watermarks: 75%/90% | Do not evict time: 120m | OOS: 95%
mirror		 Disabled
provider	 ais
versioning	 Enabled | Validate on WarmGET: no
$ ais show props bucket_name lru
PROPERTY	 VALUE
lru		 Watermarks: 75%/90% | Do not evict time: 120m | OOS: 95%
$ ais show props bucket_name lru -v
PROPERTY		 VALUE
lru.capacity_upd_time	 10m
lru.dont_evict_time	 120m
lru.enabled		 true
lru.highwm		 90
lru.lowwm		 75
lru.out_of_space	 95
```

## Set bucket props

`ais set props [OPTIONS] BUCKET_NAME JSON_SPECIFICATION|KEY=VALUE [KEY=VALUE...]`

Set bucket properties.
For the available options, see [bucket-properties](../../../docs/bucket.md#properties-and-options).

If JSON_SPECIFICATION is used, **all** properties of the bucket are set based on the values in the JSON object.

### Options

If `--reset` flag is set, arguments are ignored and bucket properties are reset to original state.

| Flag | Type | Description | Default |
| --- | --- | --- | --- |
| `--reset` | `bool` | Reset bucket properties to original state | `false` |
| `--force` | `bool` | Ignore non-critical errors | `false` |

When JSON specification is not used, some properties support user-friendly aliases:

| Property | Value alias | Description |
| --- | --- | --- |
| access | `ro` | Disables bucket modifications: denies PUT, DELETE, and ColdGET requests |
| access | `rw` | Enables object modifications: allows PUT, DELETE, and ColdGET requests |
| access | `su` | Enables full access: all `rw` permissions, bucket deletion, and changing bucket permissions |

### Examples

#### Enable mirroring for a bucket

Set the `mirror.enabled` and `mirror.copies` properties to `true` and `2` respectively, for the bucket `bucket_name`

```console
$ ais set props bucket_name 'mirror.enabled=true' 'mirror.copies=2'
Bucket props successfully updated
"mirror.enabled" set to:"true" (was:"false")
```

#### Make a bucket read-only

Set read-only access to the bucket `bucket_name`.
All PUT and DELETE requests will fail.

```console
$ ais set props bucket_name 'access=ro'
Bucket props successfully updated
"access" set to:"GET,HEAD-OBJECT,HEAD-BUCKET,LIST-OBJECTS" (was:"<PREV_ACCESS_LIST>")
```

#### Reset properties for the bucket

Reset properties for the bucket `bucket_name`.

```console
$ ais set props --reset bucket_name
Bucket props successfully reset
```

#### Connect/Disconnect AIS bucket to/from cloud bucket

Set backend bucket for AIS bucket `bucket_name` to the GCP cloud bucket `cloud_bucket`.
Once the backend bucket is set, operations (get, put, list, etc.) with `ais://bucket_name` will be exactly as we would do with `gcp://cloud_bucket`.
It's like a symlink to a cloud bucket.
The only difference is that all objects will be cached into `ais://bucket_name` (and reflected in the cloud as well) instead of `gcp://cloud_bucket`.

```console
$ ais set props bucket_name backend_bck=gcp://cloud_bucket
Bucket props successfully updated
"backend_bck.name" set to:"cloud_bucket" (was:"")
"backend_bck.provider" set to:"gcp" (was:"")
```

To disconnect cloud bucket do:

```console
$ ais set props bucket_name backend_bck=none
Bucket props successfully updated
"backend_bck.name" set to:"" (was:"cloud_bucket")
"backend_bck.provider" set to:"" (was:"gcp")
```

#### Ignore non-critical errors

To create an erasure-encoded bucket or enable EC for an existing bucket, AIS requires at least `ec.data_slices + ec.parity_slices + 1` targets.
At the same time, for small objects (size is less than `ec.objsize_limit`) it is sufficient to have only `ec.parity_slices + 1` targets.
Option `--force` allows creating erasure-encoded buckets when the number of targets is not enough but the number exceeds `ec.parity_slices`.

Note that if the number of targets is less than `ec.data_slices + ec.parity_slices + 1`, the cluster accepts only objects smaller than `ec.objsize_limit`.
Bigger objects are rejected on PUT.

In examples a cluster with 6 targets is used:

```console
$ # Creating a bucket
$ ais create bucket ais://bck --bucket-props "ec.enabled=true ec.data_slices=6 ec.parity_slices=4"
Create bucket "ais://bck" failed: EC config (6 data, 4 parity) slices requires at least 11 targets (have 6)

$ ais create bucket ais://bck --bucket-props "ec.enabled=true ec.data_slices=6 ec.parity_slices=4" --force
"ais://bck" bucket created

$ # If the number of targets is less than or equal to ec.parity_slices even `--force` does not help

$ ais set props ais://bck ec.enabled true ec.data_slices 6 ec.parity_slices 8
EC config (6 data, 8 parity)slices requires at least 15 targets (have 6). To show bucket properties, run "ais show props BUCKET_NAME -v".

$ ais set props ais://bck ec.enabled true ec.data_slices 6 ec.parity_slices 8 --force
EC config (6 data, 8 parity)slices requires at least 15 targets (have 6). To show bucket properties, run "ais show props BUCKET_NAME -v".

$ # Use force to enable EC if the number of target is sufficient to keep `ec.parity_slices+1` replicas

$ ais set props ais://bck ec.enabled true ec.data_slices 6 ec.parity_slices 4
EC config (6 data, 8 parity)slices requires at least 11 targets (have 6). To show bucket properties, run "ais show props BUCKET_NAME -v".

$ ais set props ais://bck ec.enabled true ec.data_slices 6 ec.parity_slices 4 --force
Bucket props successfully updated
"ec.enabled" set to:"true" (was:"false")
"ec.parity_slices" set to:"4" (was:"2")
```

Once erasure encoding is enabled for a bucket, the number of data and parity slices cannot be modified.
The minimum object size `ec.objsize_limit` can be changed on the fly.
To avoid accidental modification when EC for a bucket is enabled, the option `--force` must be used.

```console
$ ais set props ais://bck ec.enabled
Bucket props successfully updated
"ec.enabled" set to:"true" (was:"false")

$ ais set props ais://bck ec.objsize_limit 320000
P[dBbfp8080]: once enabled, EC configuration can be only disabled but cannot change. To show bucket properties, run "ais show props BUCKET_NAME -v".

$ ais set props ais://bck ec.objsize_limit 320000 --force
Bucket props successfully updated
"ec.objsize_limit" set to:"320000" (was:"262144")
```

#### Set bucket properties with JSON

Set **all** bucket properties for `bucket_name` bucket based on the provided JSON specification.

```bash
$ ais set props bucket_name '{
    "provider": "ais",
    "versioning": {
      "enabled": true,
      "validate_warm_get": false
    },
    "checksum": {
      "type": "xxhash",
      "validate_cold_get": true,
      "validate_warm_get": false,
      "validate_obj_move": false,
      "enable_read_range": false
    },
    "lru": {
      "lowwm": 20,
      "highwm": 80,
      "out_of_space": 90,
      "dont_evict_time": "20m",
      "capacity_upd_time": "1m",
      "enabled": true
    },
    "mirror": {
      "copies": 0,
      "burst_buffer": 0,
      "util_thresh": 0,
      "optimize_put": false,
      "enabled": false
    },
    "ec": {
        "objsize_limit": 256000,
        "data_slices": 2,
        "parity_slices": 2,
        "enabled": true
    },
    "access": "255"
}'
Bucket props successfully updated
```

```console
$ ais show props bucket_name
PROPERTY	 VALUE
access		 GET,PUT,DELETE,HEAD,ColdGET
checksum	 Type: xxhash | Validate: ColdGET
created		2020-04-08T16:20:12-08:00
ec		 2:2 (250KiB)
lru		 Watermarks: 20%/80% | Do not evict time: 120m | OOS: 90%
mirror		 Disabled
provider	 ais
versioning	 Enabled | Validate on WarmGET: no
```

If not all properties are mentioned in the JSON, the missing ones are set to zero values (empty / `false` / `nil`):

```bash
$ ais set props --reset bucket_name
Bucket props successfully reset
$ ais set props bucket_name '{
  "mirror": {
    "enabled": true,
    "copies": 2
  },
  "versioning": {
    "enabled": true,
    "validate_warm_get": true
  }
}'
Bucket props successfully updated
"versioning.validate_warm_get" set to:"true" (was:"false")
"mirror.enabled" set to:"true" (was:"false")
$ ais show props bucket_name
PROPERTY	 VALUE
access		 GET,PUT,DELETE,HEAD,ColdGET
checksum	 Type: xxhash | Validate: ColdGET
created		2020-04-08T16:20:12-08:00
ec		 Disabled
lru		 Watermarks: 75%/90% | Do not evict time: 120m | OOS: 95%
mirror		 2 copies
provider	 ais
versioning	 Enabled | Validate on WarmGET: yes
```
