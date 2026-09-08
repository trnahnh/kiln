// Runs inline at the top of <body> (app/layout.tsx) before first paint. It applies the
// same test as isHardReload() and hasSeenThisSession() in gate.ts; change both together.
// `deliveryType` is already set when this runs; `transferSize` is not, so it is never read.
// The class pauses the page's arrival animations (globals.css) until HeroScene releases
// it as the hand-off begins. The timeout is a safety net: if hydration never happens the
// page must still arrive.
export const GATE_SCRIPT = `(function(){try{
var nav=performance.getEntriesByType("navigation")[0];
var hard=!!nav&&nav.type==="reload"&&nav.deliveryType!=="cache";
var seen=false;try{seen=sessionStorage.getItem("kiln-hero-seen")==="1";}catch(e){}
var reduced=matchMedia("(prefers-reduced-motion: reduce)").matches;
if(!reduced&&(hard||!seen)){
document.documentElement.classList.add("intro-pending");
setTimeout(function(){document.documentElement.classList.remove("intro-pending");},8000);
}}catch(e){}})();`;
