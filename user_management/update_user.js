const {google} = require('googleapis');
const identitytoolkit = google.identitytoolkit('v3');
var firebaseAdmin = require('firebase-admin');
var app = firebaseAdmin.initializeApp({
    credential: firebaseAdmin.credential.applicationDefault()
}).auth()

const email = process.argv[2];
const viewer = JSON.parse(process.argv[3]);
const editor = JSON.parse(process.argv[4]);
const admin = JSON.parse(process.argv[5]);
const superadmin = process.argv[6];

console.log('email', email);
console.log('viewer', viewer);
console.log('editor', editor);
console.log('admin', admin);
console.log('superadmin', superadmin);

let user = {}
const listAllUsers = (nextPageToken) => {
    // List batch of users, 1000 at a time.
    app
        .listUsers(1000, nextPageToken)
        .then((listUsersResult) => {
            listUsersResult.users.forEach((userRecord) => {
                if (userRecord.toJSON()['email'] == email) {
                    user = userRecord.toJSON()
                    console.log(user['email'], user['displayName']);
                    console.log(JSON.stringify(user));
                    user['customClaims'] = {
                        teknoir: {
                            role: (superadmin === "true") ? "superadmin" : (admin !== "[]") ? "admin" : (editor !== "[]") ? "editor" : "viewer",
                            owner: admin,
                            editor: editor,
                            viewer: viewer
                        }
                    }
                    console.log(JSON.stringify(user));

                    app.setCustomUserClaims(user["uid"] , user['customClaims']).then(() => {
                        console.log("done!")
                    });
                }
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
listAllUsers();