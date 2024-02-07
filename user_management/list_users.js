const {google} = require('googleapis');
const identitytoolkit = google.identitytoolkit('v3');
var admin = require('firebase-admin');
var app = admin.initializeApp({
    credential: admin.credential.applicationDefault()
}).auth()

const listAllUsers = (nextPageToken) => {
    // List batch of users, 1000 at a time.
    app
        .listUsers(1000, nextPageToken)
        .then((listUsersResult) => {
            listUsersResult.users.forEach((userRecord) => {
                console.log('\t', userRecord.toJSON()['email'], userRecord.toJSON()['displayName']);
                console.log(JSON.stringify(userRecord.toJSON()));
            });
            if (listUsersResult.pageToken) {
                // List next batch of users.
                listAllUsers(listUsersResult.pageToken);
            }
        })
        .catch((error) => {
            console.log('Error listing users:', error);
        });
};

console.log('\tFirebase users:');
listAllUsers();
