export default function FacetGlyph({ className = "" }: { className?: string }) {
  return (
    <svg className={`facet-glyph ${className}`} viewBox="0 0 17 17" aria-hidden="true" fill="none">
      <path d="M8.5 1 15.5 8.5 8.5 16 1.5 8.5Z" stroke="currentColor" strokeWidth="1" strokeLinejoin="round" />
      <path d="M8.5 1 5 8.5 8.5 16M8.5 1 12 8.5 8.5 16M1.5 8.5H15.5" stroke="currentColor" strokeWidth="0.75" opacity="0.55" />
      <path d="M8.5 1 5 8.5 8.5 16 12 8.5Z" fill="currentColor" opacity="0.28" />
    </svg>
  );
}
