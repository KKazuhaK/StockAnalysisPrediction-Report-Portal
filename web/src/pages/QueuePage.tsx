import QueueTable from '../components/QueueTable'

// The Run/queue page = the full queue table with the summary stat cards on top.
// The same table (without stats) is embedded in the batch console.
export default function QueuePage() {
  return <QueueTable showStats />
}
