package volcengineutil

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
)

// Copy from https://github.com/aws/aws-sdk-go
// May have been modified by Beijing Volcanoengine Technology Ltd.

type Endpoint struct {
	//UseSSL bool
	//Locate bool
	//UseInternal                 bool
	CustomerEndpoint string
	//CustomerDomainIgnoreService bool

}

func NewEndpoint() *Endpoint {
	return &Endpoint{}
}

func (c *Endpoint) WithCustomerEndpoint(customerEndpoint string) *Endpoint {
	c.CustomerEndpoint = customerEndpoint
	return c
}

type ServiceInfo struct {
	Service string
	Region  string
}

func (c *Endpoint) GetEndpoint() string {
	if c.CustomerEndpoint != "" {
		return c.CustomerEndpoint
	} else {
		return endpoint
	}
}

const (
	separator               = "."
	openPrefix              = "open"
	endpointSuffix          = separator + "volcengineapi.com"
	dualstackEndpointSuffix = separator + "volcengine-api.com"
)

var endpoint = openPrefix + endpointSuffix

type RegionEndpointMap map[string]string

type ServiceEndpointInfo struct {
	Service        string
	IsGlobal       bool
	GlobalEndpoint string
	RegionEndpointMap
}

const (
	regionCodeCNBeijingAutoDriving  = "cn-beijing-autodriving"
	regionCodeAPSouthEast2          = "ap-southeast-2"
	regionCodeAPSouthEast3          = "ap-southeast-3"
	regionCodeCNShanghaiAutoDriving = "cn-shanghai-autodriving"
	regionCodeCNBeijingSelfdrive    = "cn-beijing-selfdrive"
)

var defaultEndpoint = map[string]*ServiceEndpointInfo{
	"mcs": {
		Service:  "mcs",
		IsGlobal: false,
	},
	"rocketmq": {
		Service:  "rocketmq",
		IsGlobal: false,
	},
	"bytehouse": {
		Service:  "bytehouse",
		IsGlobal: false,
	},
	"dns": {
		Service:  "dns",
		IsGlobal: true,
	},
	"autoscaling": {
		Service:  "autoscaling",
		IsGlobal: false,
	},
	"billing": {
		Service:  "billing",
		IsGlobal: false,
	},
	"spark": {
		Service:  "spark",
		IsGlobal: false,
	},
	"cloud_detect": {
		Service:  "cloud_detect",
		IsGlobal: false,
	},
	"filenas": {
		Service:  "filenas",
		IsGlobal: false,
	},
	"escloud": {
		Service:  "escloud",
		IsGlobal: false,
	},
	"ark": {
		Service:  "ark",
		IsGlobal: false,
	},
	"flink": {
		Service:  "flink",
		IsGlobal: false,
	},
	"cp": {
		Service:  "cp",
		IsGlobal: false,
	},
	"vefaas": {
		Service:  "vefaas",
		IsGlobal: false,
	},
	"ml_platform": {
		Service:  "ml_platform",
		IsGlobal: false,
	},
	"edx": {
		Service:  "edx",
		IsGlobal: true,
	},
	"dcdn": {
		Service:  "dcdn",
		IsGlobal: true,
	},
	"cdn": {
		Service:  "cdn",
		IsGlobal: true,
	},
	"kafka": {
		Service:  "kafka",
		IsGlobal: false,
	},
	"certificate_service": {
		Service:  "certificate_service",
		IsGlobal: true,
	},
	"waf": {
		Service:  "waf",
		IsGlobal: true,
	},
	"rds_mssql": {
		Service:  "rds_mssql",
		IsGlobal: false,
	},
	"cloudtrail": {
		Service:  "cloudtrail",
		IsGlobal: false,
	},
	"vei_api": {
		Service:  "vei_api",
		IsGlobal: true,
	},
	"cen": {
		Service:  "cen",
		IsGlobal: true,
	},
	"rabbitmq": {
		Service:  "rabbitmq",
		IsGlobal: false,
	},
	"vmp": {
		Service:  "vmp",
		IsGlobal: false,
	},
	"volc_observe": {
		Service:  "volc_observe",
		IsGlobal: false,
	},
	"dataleap": {
		Service:  "dataleap",
		IsGlobal: false,
	},
	"iam": {
		Service:  "iam",
		IsGlobal: true,
	},
	"fw_center": {
		Service:  "fw_center",
		IsGlobal: true,
	},
	"redis": {
		Service:  "redis",
		IsGlobal: false,
	},
	"mcdn": {
		Service:  "mcdn",
		IsGlobal: true,
	},
	"cloudidentity": {
		Service:  "cloudidentity",
		IsGlobal: false,
	},
	"vedbm": {
		Service:  "vedbm",
		IsGlobal: false,
	},
	"cv": {
		Service:  "cv",
		IsGlobal: true,
	},
	"translate": {
		Service:  "translate",
		IsGlobal: true,
	},
	"cloud_trail": {
		Service:  "cloud_trail",
		IsGlobal: false,
	},
	"bio": {
		Service:  "bio",
		IsGlobal: false,
	},
	"nta": {
		Service:  "nta",
		IsGlobal: true,
	},
	"elasticmapreduce": {
		Service:  "elasticmapreduce",
		IsGlobal: false,
	},
	"vepfs": {
		Service:  "vepfs",
		IsGlobal: false,
	},
	"seccenter": {
		Service:  "seccenter",
		IsGlobal: true,
	},
	"advdefence": {
		Service:  "advdefence",
		IsGlobal: true,
	},
	"tis": {
		Service:  "tis",
		IsGlobal: true,
	},
	"organization": {
		Service:  "organization",
		IsGlobal: true,
	},
	"vke": {
		Service:  "vke",
		IsGlobal: false,
	},
	"Redis": {
		Service:  "Redis",
		IsGlobal: false,
	},
	"privatelink": {
		Service:  "privatelink",
		IsGlobal: false,
	},
	"vpc": {
		Service:  "vpc",
		IsGlobal: false,
	},
	"RocketMQ": {
		Service:  "RocketMQ",
		IsGlobal: false,
	},
	"Kafka": {
		Service:  "Kafka",
		IsGlobal: false,
	},
	"rds_mysql": {
		Service:  "rds_mysql",
		IsGlobal: false,
	},
	"rds_postgresql": {
		Service:  "rds_postgresql",
		IsGlobal: false,
	},
	"storage_ebs": {
		Service:  "storage_ebs",
		IsGlobal: false,
	},
	"clb": {
		Service:  "clb",
		IsGlobal: false,
	},
	"ecs": {
		Service:  "ecs",
		IsGlobal: false,
	},
	"alb": {
		Service:  "alb",
		IsGlobal: false,
	},
	"FileNAS": {
		Service:  "FileNAS",
		IsGlobal: false,
	},
	"configcenter": {
		Service:  "configcenter",
		IsGlobal: false,
	},
	"cr": {
		Service:  "cr",
		IsGlobal: false,
	},
	"sts": {
		Service:  "sts",
		IsGlobal: false,
	},
	"mongodb": {
		Service:  "mongodb",
		IsGlobal: false,
	},
	"transitrouter": {
		Service:  "transitrouter",
		IsGlobal: false,
	},
	"Volc_Observe": {
		Service:  "Volc_Observe",
		IsGlobal: false,
	},
	"dms": {
		Service:  "dms",
		IsGlobal: false,
	},
	"auto_scaling": {
		Service:  "auto_scaling",
		IsGlobal: false,
	},
	"directconnect": {
		Service:  "directconnect",
		IsGlobal: false,
	},
	"kms": {
		Service:  "kms",
		IsGlobal: false,
	},
	"dbw": {
		Service:  "dbw",
		IsGlobal: false,
	},
	"dts": {
		Service:  "dts",
		IsGlobal: false,
	},
	"natgateway": {
		Service:  "natgateway",
		IsGlobal: false,
	},
	"tos": {
		Service:  "tos",
		IsGlobal: false,
	},
	"TLS": {
		Service:  "TLS",
		IsGlobal: false,
	},
	"vpn": {
		Service:  "vpn",
		IsGlobal: false,
	},
	"vod": {
		Service:  "vod",
		IsGlobal: false,
	},
	"quota": {
		Service:  "quota",
		IsGlobal: true,
	},
	"acep": {
		Service:  "acep",
		IsGlobal: true,
	},
	"private_zone": {
		Service:  "private_zone",
		IsGlobal: true,
	},
	"sqs": {
		Service:  "sqs",
		IsGlobal: false,
	},
	"resourcecenter": {
		Service:  "resourcecenter",
		IsGlobal: true,
	},
	"gtm": {
		Service:  "gtm",
		IsGlobal: false,
	},
}

func standardizeDomainServiceCode(serviceCode string) string {
	return strings.ReplaceAll(strings.ToLower(serviceCode), "_", "-")
}

// GetDefaultEndpointByServiceInfo retrieves the default endpoint for a given service and region.
//
// This function takes in the service name and region code, and returns a pointer to the default
// endpoint string. It checks if the service has a global endpoint or a region-specific endpoint.
// If neither is found, it returns a pointer to the default endpoint.
//
// Parameters:
// - service: The name of the service for which to retrieve the endpoint.
// - regionCode: The region code to look up the region-specific endpoint.
// - customBootstrapRegion: The map which keys are bootstrapping region code and values are empty struct.
// Returns:
// - *string: A pointer to the endpoint string. It could be a global endpoint, a region-specific
// endpoint, or a default endpoint if the specified service or region does not have a defined endpoint.
//
// Example:
//
//	endpoint := GetDefaultEndpointByServiceInfo("exampleService", "cn-beijing", nil)
//
// Note: Ensure the `defaultEndpoint` map is properly populated with service and region endpoint
// information before calling this function.
func GetDefaultEndpointByServiceInfo(service string, regionCode string,
	customBootstrapRegion map[string]struct{}, useDualStack *bool) *string {

	resultEndpoint := endpoint

	if !inBootstrapRegionList(regionCode, customBootstrapRegion) {
		return &resultEndpoint
	}

	defaultEndpointInfo, sExist := defaultEndpoint[service]
	if !sExist {
		return &resultEndpoint
	}

	suffix := endpointSuffix
	if hasEnableDualStack(useDualStack) {
		suffix = dualstackEndpointSuffix
	}

	if defaultEndpointInfo.IsGlobal {
		if len(defaultEndpointInfo.GlobalEndpoint) > 0 {
			resultEndpoint = defaultEndpointInfo.GlobalEndpoint
			return &resultEndpoint
		}
		resultEndpoint = standardizeDomainServiceCode(service) + suffix
		return &resultEndpoint
	}

	// regional endpoint
	regionEndpointMp := defaultEndpointInfo.RegionEndpointMap
	if regionEndpointMp != nil {
		regionEndpointStr, rExist := regionEndpointMp[regionCode]
		if rExist {
			resultEndpoint = regionEndpointStr
			return &resultEndpoint
		}
	}

	resultEndpoint = standardizeDomainServiceCode(service) + separator + regionCode + suffix
	return &resultEndpoint

}

var bootstrapRegion = map[string]struct{}{
	regionCodeCNBeijingAutoDriving:  {},
	regionCodeAPSouthEast2:          {},
	regionCodeAPSouthEast3:          {},
	regionCodeCNShanghaiAutoDriving: {},
	regionCodeCNBeijingSelfdrive:    {},
}

func inBootstrapRegionList(regionCode string, customBootstrapRegion map[string]struct{}) bool {
	regionCode = strings.TrimSpace(regionCode)
	bsRegionListPath := os.Getenv("VOLC_BOOTSTRAP_REGION_LIST_CONF")
	if len(bsRegionListPath) > 0 {
		f, err := ioutil.ReadFile(filepath.Clean(bsRegionListPath))
		if err == nil {
			for _, l := range strings.Split(string(f), "\n") {
				l = strings.TrimSpace(l)
				if len(l) == 0 {
					continue
				}
				if l == regionCode {
					return true
				}
			}
		}
	}

	if len(bootstrapRegion) > 0 {
		_, ok := bootstrapRegion[regionCode]
		if ok {
			return ok
		}
	}

	if len(customBootstrapRegion) > 0 {
		_, ok := customBootstrapRegion[regionCode]
		return ok
	}

	return false
}

func hasEnableDualStack(useDualStack *bool) bool {
	if useDualStack == nil {
		return os.Getenv("VOLC_ENABLE_DUALSTACK") == "true"
	}
	return *useDualStack
}
