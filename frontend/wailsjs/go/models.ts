export namespace main {
	
	export class MatchConfig {
	    fileAPath: string;
	    fileBPath: string;
	    colAMatchIndex: number;
	    colATimeIndex: number;
	    colBMatchIndex: number;
	    colBTimeIndex: number;
	    colBExtractIndex: number;
	    regexPattern: string;
	    timeWindow: number;
	    threshold: number;
	    allMatches: boolean;
	    caseSensitive: boolean;
	    sortBy: string;
	    maxPreview: number;
	    exportFormat: string;
	    includeHeader: boolean;
	
	    static createFrom(source: any = {}) {
	        return new MatchConfig(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.fileAPath = source["fileAPath"];
	        this.fileBPath = source["fileBPath"];
	        this.colAMatchIndex = source["colAMatchIndex"];
	        this.colATimeIndex = source["colATimeIndex"];
	        this.colBMatchIndex = source["colBMatchIndex"];
	        this.colBTimeIndex = source["colBTimeIndex"];
	        this.colBExtractIndex = source["colBExtractIndex"];
	        this.regexPattern = source["regexPattern"];
	        this.timeWindow = source["timeWindow"];
	        this.threshold = source["threshold"];
	        this.allMatches = source["allMatches"];
	        this.caseSensitive = source["caseSensitive"];
	        this.sortBy = source["sortBy"];
	        this.maxPreview = source["maxPreview"];
	        this.exportFormat = source["exportFormat"];
	        this.includeHeader = source["includeHeader"];
	    }
	}
	export class MatchResult {
	    rowAData: string[];
	    rowBKey: string;
	    extractValue: string;
	    monthlyCellName: string;
	    dailyCellId: string;
	    interruptReason: string;
	    timeDiff: string;
	    similarityScore: number;
	    aiMatched: boolean;
	
	    static createFrom(source: any = {}) {
	        return new MatchResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.rowAData = source["rowAData"];
	        this.rowBKey = source["rowBKey"];
	        this.extractValue = source["extractValue"];
	        this.monthlyCellName = source["monthlyCellName"];
	        this.dailyCellId = source["dailyCellId"];
	        this.interruptReason = source["interruptReason"];
	        this.timeDiff = source["timeDiff"];
	        this.similarityScore = source["similarityScore"];
	        this.aiMatched = source["aiMatched"];
	    }
	}

}

