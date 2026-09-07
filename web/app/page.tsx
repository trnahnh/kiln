import Header from "@/components/Header";
import Hero from "@/components/Hero";
import Flow from "@/components/Flow";
import Invariants from "@/components/Invariants";
import ValidationTable from "@/components/ValidationTable";
import Footer from "@/components/Footer";
import ReplayIntro from "@/components/ReplayIntro";

export default function Page() {
  return (
    <>
      <link rel="preload" href="/hero/lattice.json" as="fetch" crossOrigin="anonymous" />
      <Header />
      <main>
        <Hero />
        <Flow />
        <Invariants />
        <ValidationTable />
      </main>
      <Footer />
      <ReplayIntro />
    </>
  );
}
