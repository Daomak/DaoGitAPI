export namespace main {
	
	export class Tokens {
	    github: string;
	    gitee: string;
	
	    static createFrom(source: any = {}) {
	        return new Tokens(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.github = source["github"];
	        this.gitee = source["gitee"];
	    }
	}

}

