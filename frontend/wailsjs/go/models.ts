export namespace config {
	
	export class ProxySettings {
	    enabled: boolean;
	    mode: string;
	    url: string;
	    protocol: string;
	
	    static createFrom(source: any = {}) {
	        return new ProxySettings(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.enabled = source["enabled"];
	        this.mode = source["mode"];
	        this.url = source["url"];
	        this.protocol = source["protocol"];
	    }
	}
	export class AppSettings {
	    theme: string;
	    language: string;
	    proxy: ProxySettings;
	    endpoints: Record<string, string>;
	    installPath: string;
	    githubMirror: string;
	    githubToken: string;
	    downloadThreads: number;
	
	    static createFrom(source: any = {}) {
	        return new AppSettings(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.theme = source["theme"];
	        this.language = source["language"];
	        this.proxy = this.convertValues(source["proxy"], ProxySettings);
	        this.endpoints = source["endpoints"];
	        this.installPath = source["installPath"];
	        this.githubMirror = source["githubMirror"];
	        this.githubToken = source["githubToken"];
	        this.downloadThreads = source["downloadThreads"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

export namespace logger {
	
	export class LogFileInfo {
	    name: string;
	    size: number;
	    modTime: string;
	
	    static createFrom(source: any = {}) {
	        return new LogFileInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.size = source["size"];
	        this.modTime = source["modTime"];
	    }
	}

}

export namespace pathmgr {
	
	export class PathEntry {
	    path: string;
	    isManaged: boolean;
	    sdkType: string;
	    source: string;
	    systemProtected: boolean;
	    externalManager: string;
	
	    static createFrom(source: any = {}) {
	        return new PathEntry(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.isManaged = source["isManaged"];
	        this.sdkType = source["sdkType"];
	        this.source = source["source"];
	        this.systemProtected = source["systemProtected"];
	        this.externalManager = source["externalManager"];
	    }
	}

}

export namespace sdk {
	
	export class EndpointInfo {
	    sdkType: string;
	    displayName: string;
	    defaultEndpoint: string;
	
	    static createFrom(source: any = {}) {
	        return new EndpointInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.sdkType = source["sdkType"];
	        this.displayName = source["displayName"];
	        this.defaultEndpoint = source["defaultEndpoint"];
	    }
	}
	export class GlobalPackage {
	    name: string;
	    version: string;
	
	    static createFrom(source: any = {}) {
	        return new GlobalPackage(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.version = source["version"];
	    }
	}
	export class PackageManagerInfo {
	    name: string;
	    version: string;
	    installed: boolean;
	    parentSdk: string;
	
	    static createFrom(source: any = {}) {
	        return new PackageManagerInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.version = source["version"];
	        this.installed = source["installed"];
	        this.parentSdk = source["parentSdk"];
	    }
	}
	export class SdkStatus {
	    sdkType: string;
	    displayName: string;
	    configured: boolean;
	    pathConfigured: boolean;
	    pathVersion: string;
	    currentVersion: string;
	    installedVersions: string[];
	    installPath: string;
	    needsSwitch: boolean;
	    systemProtected: boolean;
	    systemPath: string;
	    externalManager: string;
	    pathBinary: string;
	
	    static createFrom(source: any = {}) {
	        return new SdkStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.sdkType = source["sdkType"];
	        this.displayName = source["displayName"];
	        this.configured = source["configured"];
	        this.pathConfigured = source["pathConfigured"];
	        this.pathVersion = source["pathVersion"];
	        this.currentVersion = source["currentVersion"];
	        this.installedVersions = source["installedVersions"];
	        this.installPath = source["installPath"];
	        this.needsSwitch = source["needsSwitch"];
	        this.systemProtected = source["systemProtected"];
	        this.systemPath = source["systemPath"];
	        this.externalManager = source["externalManager"];
	        this.pathBinary = source["pathBinary"];
	    }
	}
	export class VersionInfo {
	    version: string;
	    major: number;
	    downloadUrl: string;
	    fileName: string;
	    isLts: boolean;
	    releaseDate: string;
	
	    static createFrom(source: any = {}) {
	        return new VersionInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.version = source["version"];
	        this.major = source["major"];
	        this.downloadUrl = source["downloadUrl"];
	        this.fileName = source["fileName"];
	        this.isLts = source["isLts"];
	        this.releaseDate = source["releaseDate"];
	    }
	}

}

export namespace storage {
	
	export class StorageInfo {
	    sdkType: string;
	    displayName: string;
	    sdkDir: string;
	    totalSize: number;
	    versionCount: number;
	    activeVer: string;
	
	    static createFrom(source: any = {}) {
	        return new StorageInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.sdkType = source["sdkType"];
	        this.displayName = source["displayName"];
	        this.sdkDir = source["sdkDir"];
	        this.totalSize = source["totalSize"];
	        this.versionCount = source["versionCount"];
	        this.activeVer = source["activeVer"];
	    }
	}

}

export namespace update {
	
	export class AppInfo {
	    version: string;
	    goVersion: string;
	    license: string;
	    repoUrl: string;
	    updateUrl: string;
	
	    static createFrom(source: any = {}) {
	        return new AppInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.version = source["version"];
	        this.goVersion = source["goVersion"];
	        this.license = source["license"];
	        this.repoUrl = source["repoUrl"];
	        this.updateUrl = source["updateUrl"];
	    }
	}
	export class UpdateInfo {
	    hasUpdate: boolean;
	    latestVersion: string;
	    changelog: string;
	    downloadUrl: string;
	    filename: string;
	    sha256: string;
	
	    static createFrom(source: any = {}) {
	        return new UpdateInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.hasUpdate = source["hasUpdate"];
	        this.latestVersion = source["latestVersion"];
	        this.changelog = source["changelog"];
	        this.downloadUrl = source["downloadUrl"];
	        this.filename = source["filename"];
	        this.sha256 = source["sha256"];
	    }
	}

}

