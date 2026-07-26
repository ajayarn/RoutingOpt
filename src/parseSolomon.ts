import { Customer, VRPTWInstance } from './types';

// Parses the standard Solomon VRPTW benchmark text format (name / VEHICLE
// NUMBER+CAPACITY / CUSTOMER table). Ported from server.ts's parseSolomonText
// so the frontend can list/load instances without a backend - GitHub Pages
// (and any other static host) has no server to ask.
export function parseSolomonText(content: string): VRPTWInstance {
  const lines = content.split('\n');
  const customers: Customer[] = [];
  let name = '';
  let capacity = 200;
  let vehicleNumber = 25;
  let state = 0; // 0: header, 1: vehicle, 2: customer-header, 3: customer-data

  for (let line of lines) {
    line = line.trim();
    if (!line) continue;

    if (!name && state === 0) {
      name = line;
      continue;
    }

    if (line.includes('VEHICLE')) {
      state = 1;
      continue;
    }
    if (line.includes('CUSTOMER')) {
      state = 2;
      continue;
    }

    const fields = line.split(/\s+/);
    if (fields.length === 0) continue;

    if (state === 1) {
      if (fields[0] === 'NUMBER' || fields[0] === 'CAPACITY') continue;
      const num = parseInt(fields[0]);
      const capVal = parseFloat(fields[1]);
      if (!isNaN(num) && !isNaN(capVal)) {
        vehicleNumber = num;
        capacity = capVal;
        state = 0;
      }
    } else if (state === 2) {
      if (line.includes('CUST NO.') || line.includes('XCOORD')) continue;
      state = 3;
    }

    if (state === 3) {
      if (fields.length < 7) continue;
      const id = parseInt(fields[0]);
      const x = parseFloat(fields[1]);
      const y = parseFloat(fields[2]);
      const demand = parseFloat(fields[3]);
      const readyTime = parseFloat(fields[4]);
      const dueDate = parseFloat(fields[5]);
      const serviceTime = parseFloat(fields[6]);

      if (!isNaN(id) && !isNaN(x) && !isNaN(y) && !isNaN(demand) && !isNaN(readyTime) && !isNaN(dueDate) && !isNaN(serviceTime)) {
        customers.push({ id, x, y, demand, readyTime, dueDate, serviceTime });
      }
    }
  }

  const depot = customers.find(c => c.id === 0) || { id: 0, x: 40, y: 50, demand: 0, readyTime: 0, dueDate: 1236, serviceTime: 0 };
  const filteredCustomers = customers.filter(c => c.id !== 0);

  return {
    name,
    vehicleNumber,
    capacity,
    depot,
    customers: filteredCustomers
  };
}
