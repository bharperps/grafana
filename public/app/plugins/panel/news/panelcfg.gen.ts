// Code generated - EDITING IS FUTILE. DO NOT EDIT.
//
// Generated by:
//     public/app/plugins/gen.go
// Using jennies:
//     TSTypesJenny
//     PluginTSTypesJenny
//
// Run 'make gen-cue' from repository root to regenerate.

export interface Options {
  /**
   * empty/missing will default to grafana blog
   */
  feedUrl?: string;
  showImage?: boolean;
}

export const defaultOptions: Partial<Options> = {
  showImage: true,
};
